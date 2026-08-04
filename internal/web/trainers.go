package web

import (
	"net/http"
	"strconv"

	"familyhub/internal/model"
)

type kindOption struct {
	Code  string
	Label string
}

var kindOptions = []kindOption{
	{model.AbsenceVacation, "отпуск"},
	{model.AbsenceSick, "болезнь"},
	{model.AbsenceOther, "другое"},
}

type trainerWithAbsences struct {
	Trainer model.Trainer
	Current []model.TrainerAbsence // date_to >= today: active or upcoming
	Past    []model.TrainerAbsence
}

type trainersData struct {
	Trainers []trainerWithAbsences
	Kinds    []kindOption
	Today    string
	Error    string
}

func (a *App) handleTrainers(w http.ResponseWriter, r *http.Request) {
	a.renderTrainers(w, "")
}

func (a *App) renderTrainers(w http.ResponseWriter, errMsg string) {
	trainers, err := a.Store.ListTrainers()
	if err != nil {
		a.serverError(w, err)
		return
	}
	absences, err := a.Store.ListAllAbsences()
	if err != nil {
		a.serverError(w, err)
		return
	}
	byTrainer := make(map[int64][]model.TrainerAbsence)
	for _, ab := range absences {
		byTrainer[ab.TrainerID] = append(byTrainer[ab.TrainerID], ab)
	}
	td := today()
	data := trainersData{Kinds: kindOptions, Today: td, Error: errMsg}
	for _, t := range trainers {
		x := trainerWithAbsences{Trainer: t}
		for _, ab := range byTrainer[t.ID] {
			if ab.DateTo >= td {
				x.Current = append(x.Current, ab)
			} else {
				x.Past = append(x.Past, ab)
			}
		}
		data.Trainers = append(data.Trainers, x)
	}
	a.render(w, "trainers.html", "Тренеры", "trainers", data)
}

func (a *App) handleAbsenceCreate(w http.ResponseWriter, r *http.Request) {
	trainerID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := r.ParseForm(); err != nil {
		a.serverError(w, err)
		return
	}
	from := r.FormValue("date_from")
	to := r.FormValue("date_to")
	kind := r.FormValue("kind")
	comment := normalizeName(r.FormValue("comment"))
	if _, err := model.ParseDate(from); err != nil {
		a.renderTrainers(w, "укажи корректные даты")
		return
	}
	if _, err := model.ParseDate(to); err != nil {
		a.renderTrainers(w, "укажи корректные даты")
		return
	}
	if err := a.Store.CreateAbsence(trainerID, from, to, kind, comment); err != nil {
		a.renderTrainers(w, err.Error())
		return
	}
	http.Redirect(w, r, "/trainers", http.StatusSeeOther)
}

func (a *App) handleAbsenceDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("absenceId"), 10, 64)
	if err := a.Store.DeleteAbsence(id); err != nil {
		a.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/trainers", http.StatusSeeOther)
}
