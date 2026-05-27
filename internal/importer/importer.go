package importer

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

var statusMap = map[string]string{
	"проведено":  "done",
	"перенесено": "rescheduled",
	"отменено":   "cancelled",
	"отмненео":   "cancelled",
	"пропущено":  "skipped",
}

const (
	visitsSheet   = "visitsform"
	currentSheet  = "current"
	paymentsSheet = "payments"
)

// suspicious year: rows in the source file start no earlier than 2025-11-27.
// Anything more than 90 days before that is almost certainly a typo, in which
// case we fall back to the form-submission timestamp (column A).
var earliestValid = mustDate("2025-11-27")

type Stats struct {
	Persons     int
	Enrollments int
	Visits      int
	Payments    int
	DateFixed   int
	StatusFixed int
	Skipped     int
}

// enrollKey identifies an enrollment by owner + class name.
func enrollKey(personID int64, name string) string {
	return strconv.FormatInt(personID, 10) + "\x00" + name
}

func Import(database *sql.DB, xlsxPath string, logger *slog.Logger) (Stats, error) {
	var stats Stats

	f, err := excelize.OpenFile(xlsxPath)
	if err != nil {
		return stats, fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()

	tx, err := database.Begin()
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	persons, err := loadPersons(tx)
	if err != nil {
		return stats, fmt.Errorf("load persons: %w", err)
	}

	enrollments, n, err := upsertEnrollments(tx, f, persons, logger)
	if err != nil {
		return stats, fmt.Errorf("enrollments: %w", err)
	}
	stats.Enrollments = n
	stats.Persons = len(persons)

	if _, err := tx.Exec("DELETE FROM visits"); err != nil {
		return stats, err
	}
	if _, err := tx.Exec("DELETE FROM payments"); err != nil {
		return stats, err
	}

	vs, err := importVisits(tx, f, persons, enrollments, logger, &stats)
	if err != nil {
		return stats, fmt.Errorf("visits: %w", err)
	}
	stats.Visits = vs

	ps, err := importPayments(tx, f, persons, enrollments, logger, &stats)
	if err != nil {
		return stats, fmt.Errorf("payments: %w", err)
	}
	stats.Payments = ps

	if err := tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func loadPersons(tx execer) (map[string]int64, error) {
	rows, err := tx.Query("SELECT id, name FROM persons")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}

func getOrCreatePerson(tx execer, persons map[string]int64, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if id, ok := persons[name]; ok {
		return id, nil
	}
	res, err := tx.Exec("INSERT INTO persons (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	persons[name] = id
	return id, nil
}

func upsertEnrollments(tx execer, f *excelize.File, persons map[string]int64, logger *slog.Logger) (map[string]int64, int, error) {
	rows, err := f.GetRows(currentSheet)
	if err != nil {
		return nil, 0, err
	}
	enrollments := map[string]int64{}
	count := 0
	for i, row := range rows {
		if i == 0 || len(row) < 6 {
			continue
		}
		personName := strings.TrimSpace(row[0])
		className := strings.TrimSpace(row[1])
		priceStr := strings.TrimSpace(row[5])
		if personName == "" || className == "" || priceStr == "" {
			continue
		}
		personID, err := getOrCreatePerson(tx, persons, personName)
		if err != nil {
			return nil, 0, err
		}
		price, err := strconv.ParseFloat(strings.ReplaceAll(priceStr, ",", "."), 64)
		if err != nil {
			logger.Warn("bad price", "person", personName, "class", className, "value", priceStr)
			continue
		}

		var id int64
		err = tx.QueryRow(`SELECT id FROM enrollments WHERE person_id=? AND name=?`, personID, className).Scan(&id)
		switch {
		case err == nil:
			if _, err := tx.Exec(`UPDATE enrollments SET current_price=? WHERE id=?`, price, id); err != nil {
				return nil, 0, err
			}
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.Exec(`
				INSERT INTO enrollments (person_id, name, billing_type, current_price)
				VALUES (?, ?, 'per_lesson', ?)`, personID, className, price)
			if err != nil {
				return nil, 0, fmt.Errorf("insert enrollment %s/%s: %w", personName, className, err)
			}
			id, err = res.LastInsertId()
			if err != nil {
				return nil, 0, err
			}
		default:
			return nil, 0, err
		}
		enrollments[enrollKey(personID, className)] = id
		count++
	}
	return enrollments, count, nil
}

func importVisits(tx execer, f *excelize.File, persons, enrollments map[string]int64, logger *slog.Logger, stats *Stats) (int, error) {
	rows, err := f.Rows(visitsSheet)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	rowIdx := 0
	for rows.Next() {
		rowIdx++
		cells, err := rows.Columns(excelize.Options{RawCellValue: true})
		if err != nil {
			return 0, err
		}
		if rowIdx == 1 || len(cells) < 6 {
			continue
		}
		tsRaw := strings.TrimSpace(cells[0])
		dateRaw := strings.TrimSpace(cells[1])
		className := strings.TrimSpace(cells[2])
		statusRaw := strings.TrimSpace(cells[3])
		comment := ""
		if len(cells) > 4 {
			comment = strings.TrimSpace(cells[4])
		}
		personName := strings.TrimSpace(cells[5])

		if dateRaw == "" || className == "" || statusRaw == "" || personName == "" {
			stats.Skipped++
			continue
		}

		ts, _ := parseExcelTime(tsRaw)
		date, err := parseExcelTime(dateRaw)
		if err != nil {
			logger.Warn("bad visit date", "row", rowIdx, "raw", dateRaw)
			stats.Skipped++
			continue
		}
		if date.Before(earliestValid.AddDate(0, 0, -90)) && !ts.IsZero() {
			logger.Info("fix visit date", "row", rowIdx, "from", date.Format("2006-01-02"), "to", ts.Format("2006-01-02"))
			date = ts
			stats.DateFixed++
		}

		status, ok := statusMap[strings.ToLower(statusRaw)]
		if !ok {
			logger.Warn("unknown status", "row", rowIdx, "raw", statusRaw)
			stats.Skipped++
			continue
		}
		if strings.ToLower(statusRaw) == "отмненео" {
			stats.StatusFixed++
		}

		personID, ok := persons[personName]
		if !ok {
			logger.Warn("unknown person in visit", "row", rowIdx, "name", personName)
			stats.Skipped++
			continue
		}
		enrollmentID, ok := enrollments[enrollKey(personID, className)]
		if !ok {
			logger.Warn("no enrollment for visit", "row", rowIdx, "person", personName, "class", className)
			stats.Skipped++
			continue
		}

		_, err = tx.Exec(`
			INSERT INTO visits (enrollment_id, date, status, comment, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			enrollmentID, date.Format("2006-01-02"), status, comment, ts.Format(time.RFC3339))
		if err != nil {
			return 0, fmt.Errorf("insert visit row %d: %w", rowIdx, err)
		}
		count++
	}
	return count, rows.Error()
}

func importPayments(tx execer, f *excelize.File, persons, enrollments map[string]int64, logger *slog.Logger, stats *Stats) (int, error) {
	rows, err := f.Rows(paymentsSheet)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	rowIdx := 0
	for rows.Next() {
		rowIdx++
		cells, err := rows.Columns(excelize.Options{RawCellValue: true})
		if err != nil {
			return 0, err
		}
		if rowIdx == 1 || len(cells) < 5 {
			continue
		}
		dateRaw := strings.TrimSpace(cells[0])
		personName := strings.TrimSpace(cells[1])
		className := strings.TrimSpace(cells[2])
		lessonsRaw := strings.TrimSpace(cells[3])
		amountRaw := strings.TrimSpace(cells[4])
		comment := ""
		if len(cells) > 5 {
			comment = strings.TrimSpace(cells[5])
		}

		if dateRaw == "" || personName == "" || className == "" {
			continue
		}

		date, err := parseExcelTime(dateRaw)
		if err != nil {
			logger.Warn("bad payment date", "row", rowIdx, "raw", dateRaw)
			stats.Skipped++
			continue
		}
		lessons, err := strconv.ParseFloat(strings.ReplaceAll(lessonsRaw, ",", "."), 64)
		if err != nil {
			logger.Warn("bad lessons count", "row", rowIdx, "raw", lessonsRaw)
			stats.Skipped++
			continue
		}
		amount, err := strconv.ParseFloat(strings.ReplaceAll(amountRaw, ",", "."), 64)
		if err != nil {
			logger.Warn("bad amount", "row", rowIdx, "raw", amountRaw)
			stats.Skipped++
			continue
		}

		personID, ok := persons[personName]
		if !ok {
			logger.Warn("unknown person in payment", "row", rowIdx, "name", personName)
			stats.Skipped++
			continue
		}
		enrollmentID, ok := enrollments[enrollKey(personID, className)]
		if !ok {
			logger.Warn("no enrollment for payment", "row", rowIdx, "person", personName, "class", className)
			stats.Skipped++
			continue
		}

		_, err = tx.Exec(`
			INSERT INTO payments (enrollment_id, date, amount, lessons_paid, comment)
			VALUES (?, ?, ?, ?, ?)`,
			enrollmentID, date.Format("2006-01-02"), amount, int64(lessons), comment)
		if err != nil {
			return 0, fmt.Errorf("insert payment row %d: %w", rowIdx, err)
		}
		count++
	}
	return count, rows.Error()
}

// parseExcelTime handles both Excel serial date numbers and ISO-like strings.
func parseExcelTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		t, err := excelize.ExcelDateToTime(n, false)
		if err != nil {
			return time.Time{}, err
		}
		return t, nil
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02",
		"01/02/2006",
		"02.01.2006",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable date %q", raw)
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}
