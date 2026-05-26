package importer

import (
	"database/sql"
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
	Enrollments int
	Visits      int
	Payments    int
	DateFixed   int
	StatusFixed int
	Skipped     int
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

	children, err := loadNameMap(tx, "children")
	if err != nil {
		return stats, fmt.Errorf("load children: %w", err)
	}
	activities, err := loadNameMap(tx, "activities")
	if err != nil {
		return stats, fmt.Errorf("load activities: %w", err)
	}

	enrollments, n, err := upsertEnrollments(tx, f, children, activities, logger)
	if err != nil {
		return stats, fmt.Errorf("enrollments: %w", err)
	}
	stats.Enrollments = n

	if _, err := tx.Exec("DELETE FROM visits"); err != nil {
		return stats, err
	}
	if _, err := tx.Exec("DELETE FROM payments"); err != nil {
		return stats, err
	}

	vs, err := importVisits(tx, f, children, activities, enrollments, logger, &stats)
	if err != nil {
		return stats, fmt.Errorf("visits: %w", err)
	}
	stats.Visits = vs

	ps, err := importPayments(tx, f, children, activities, enrollments, logger, &stats)
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

func loadNameMap(tx execer, table string) (map[string]int64, error) {
	rows, err := tx.Query("SELECT id, name FROM " + table)
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

func upsertEnrollments(tx execer, f *excelize.File, children, activities map[string]int64, logger *slog.Logger) (map[[2]int64]int64, int, error) {
	rows, err := f.GetRows(currentSheet)
	if err != nil {
		return nil, 0, err
	}
	enrollments := map[[2]int64]int64{}
	count := 0
	for i, row := range rows {
		if i == 0 {
			continue
		}
		if len(row) < 6 {
			continue
		}
		childName := strings.TrimSpace(row[0])
		activityName := strings.TrimSpace(row[1])
		priceStr := strings.TrimSpace(row[5])
		if childName == "" || activityName == "" || priceStr == "" {
			continue
		}
		childID, ok := children[childName]
		if !ok {
			logger.Warn("unknown child in current", "name", childName)
			continue
		}
		activityID, ok := activities[activityName]
		if !ok {
			logger.Warn("unknown activity in current", "name", activityName)
			continue
		}
		price, err := strconv.ParseFloat(strings.ReplaceAll(priceStr, ",", "."), 64)
		if err != nil {
			logger.Warn("bad price", "child", childName, "activity", activityName, "value", priceStr)
			continue
		}
		_, err = tx.Exec(`
			INSERT INTO enrollments (child_id, activity_id, billing_type, current_price)
			VALUES (?, ?, 'per_lesson', ?)
			ON CONFLICT(child_id, activity_id) DO UPDATE SET current_price = excluded.current_price
		`, childID, activityID, price)
		if err != nil {
			return nil, 0, fmt.Errorf("upsert enrollment %s/%s: %w", childName, activityName, err)
		}
		var id int64
		if err := tx.QueryRow(`SELECT id FROM enrollments WHERE child_id=? AND activity_id=?`, childID, activityID).Scan(&id); err != nil {
			return nil, 0, err
		}
		enrollments[[2]int64{childID, activityID}] = id
		count++
	}
	return enrollments, count, nil
}

func importVisits(tx execer, f *excelize.File, children, activities map[string]int64, enrollments map[[2]int64]int64, logger *slog.Logger, stats *Stats) (int, error) {
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
		if rowIdx == 1 {
			continue
		}
		if len(cells) < 6 {
			continue
		}
		tsRaw := strings.TrimSpace(cells[0])
		dateRaw := strings.TrimSpace(cells[1])
		activityName := strings.TrimSpace(cells[2])
		statusRaw := strings.TrimSpace(cells[3])
		comment := ""
		if len(cells) > 4 {
			comment = strings.TrimSpace(cells[4])
		}
		childName := strings.TrimSpace(cells[5])

		if dateRaw == "" || activityName == "" || statusRaw == "" || childName == "" {
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

		childID, ok := children[childName]
		if !ok {
			logger.Warn("unknown child in visit", "row", rowIdx, "name", childName)
			stats.Skipped++
			continue
		}
		activityID, ok := activities[activityName]
		if !ok {
			logger.Warn("unknown activity in visit", "row", rowIdx, "name", activityName)
			stats.Skipped++
			continue
		}
		enrollmentID, ok := enrollments[[2]int64{childID, activityID}]
		if !ok {
			logger.Warn("no enrollment for visit", "row", rowIdx, "child", childName, "activity", activityName)
			stats.Skipped++
			continue
		}

		_, err = tx.Exec(`
			INSERT INTO visits (enrollment_id, date, status, comment, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, enrollmentID, date.Format("2006-01-02"), status, comment, ts.Format(time.RFC3339))
		if err != nil {
			return 0, fmt.Errorf("insert visit row %d: %w", rowIdx, err)
		}
		count++
	}
	return count, rows.Error()
}

func importPayments(tx execer, f *excelize.File, children, activities map[string]int64, enrollments map[[2]int64]int64, logger *slog.Logger, stats *Stats) (int, error) {
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
		if rowIdx == 1 {
			continue
		}
		if len(cells) < 5 {
			continue
		}
		dateRaw := strings.TrimSpace(cells[0])
		childName := strings.TrimSpace(cells[1])
		activityName := strings.TrimSpace(cells[2])
		lessonsRaw := strings.TrimSpace(cells[3])
		amountRaw := strings.TrimSpace(cells[4])
		comment := ""
		if len(cells) > 5 {
			comment = strings.TrimSpace(cells[5])
		}

		if dateRaw == "" || childName == "" || activityName == "" {
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

		childID, ok := children[childName]
		if !ok {
			logger.Warn("unknown child in payment", "row", rowIdx, "name", childName)
			stats.Skipped++
			continue
		}
		activityID, ok := activities[activityName]
		if !ok {
			logger.Warn("unknown activity in payment", "row", rowIdx, "name", activityName)
			stats.Skipped++
			continue
		}
		enrollmentID, ok := enrollments[[2]int64{childID, activityID}]
		if !ok {
			logger.Warn("no enrollment for payment", "row", rowIdx, "child", childName, "activity", activityName)
			stats.Skipped++
			continue
		}

		_, err = tx.Exec(`
			INSERT INTO payments (enrollment_id, date, amount, lessons_paid, comment)
			VALUES (?, ?, ?, ?, ?)
		`, enrollmentID, date.Format("2006-01-02"), amount, int64(lessons), comment)
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
