package model

import (
	"strings"
	"testing"
)

func TestCourseNameDropsTheClassNumber(t *testing.T) {
	cases := map[string]string{
		"Алгебра 9":            "Алгебра",
		"Фізика 9 ":            "Фізика",
		"Українська мова 9":    "Українська мова",
		"Основи здоров'я":      "Основи здоров'я",
		"Інформатика 9 клас":   "Інформатика 9 клас",
		"9":                    "9",
		"Англійська мова 9 10": "Англійська мова 9",
	}
	for in, want := range cases {
		if got := CourseName(in); got != want {
			t.Errorf("CourseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortItemName(t *testing.T) {
	cases := map[string]string{
		"Оцінка вчителя 12":        "ОВ 12",
		"Контрольна робота 3":      "КР 3",
		" Оцінка вчителя 7 ":       "ОВ 7",
		"Тест: квадратні рівняння": "Тест: квадратні рівняння",
		"Оцінка": "Оцінка",
	}
	for in, want := range cases {
		if got := ShortItemName(in); got != want {
			t.Errorf("ShortItemName(%q) = %q, want %q", in, got, want)
		}
	}

	long := strings.Repeat("Лабораторна робота ", 5)
	got := ShortItemName(long)
	if n := len([]rune(got)); n > maxItemName {
		t.Fatalf("ShortItemName kept %d characters: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("a cut name does not show it was cut: %q", got)
	}
}

func TestFormatGrade(t *testing.T) {
	cases := map[float64]string{
		10:      "10",
		7.8:     "7,8",
		2.2:     "2,2",
		5.54545: "5,5",
		11.96:   "12",
		0:       "0",
	}
	for in, want := range cases {
		if got := FormatGrade(in); got != want {
			t.Errorf("FormatGrade(%v) = %q, want %q", in, got, want)
		}
	}
}
