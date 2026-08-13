package store

// ClaimBillingReminder reserves the right to warn about one coverage period
// ending, and reports whether this caller got it. The insert is the claim:
// INSERT OR IGNORE against the primary key makes "check then send" atomic, so
// two ticks racing past the same period cannot both send.
//
// coversUntil, not the send date, is the key — the warning window spans
// several days, and a restart inside it must not produce a second message.
func (s *Store) ClaimBillingReminder(enrollmentID int64, coversUntil string) (bool, error) {
	res, err := s.db.Exec(`
		INSERT OR IGNORE INTO billing_reminders (enrollment_id, covers_until)
		VALUES (?, ?)`, enrollmentID, coversUntil)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
