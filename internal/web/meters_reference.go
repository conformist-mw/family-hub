package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"familyhub/internal/model"
	"familyhub/internal/store"
)

// The utilities world's reference data: what you are billed for, at what
// price, and where. Changed a couple of times a year, which is why it stays
// web-only and never reaches the Mini App.
//
// Every list shows archived rows too, marked. Archiving hides a utility from
// the month view; a page whose whole job is to manage them must still show
// what there is to un-archive.

func (a *App) handleMeterTariffs(w http.ResponseWriter, r *http.Request) {
	tariffs, err := a.Store.TariffsAll()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "meters_tariffs.html", "Тарифи", "tariffs", tariffs)
}

func (a *App) handleMeterUtilities(w http.ResponseWriter, r *http.Request) {
	utilities, err := a.Store.UtilitiesAll()
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "meters_utilities.html", "Сервіси", "utilities", utilities)
}

// addressRow is an address plus what archiving it would take with it.
type addressRow struct {
	model.Address
	Utilities int
}

func (a *App) handleMeterAddresses(w http.ResponseWriter, r *http.Request) {
	addresses, err := a.Store.AddressesAll()
	if err != nil {
		a.serverError(w, err)
		return
	}
	rows := make([]addressRow, 0, len(addresses))
	for _, ad := range addresses {
		n, err := a.Store.UtilitiesCountForAddress(ad.ID)
		if err != nil {
			a.serverError(w, err)
			return
		}
		rows = append(rows, addressRow{Address: ad, Utilities: n})
	}
	a.render(w, "meters_addresses.html", "Адреси", "addresses", rows)
}

// ── forms ────────────────────────────────────────────────────────────────────
//
// Three entities, one shape: a list with an edit link per row and a toggle,
// and a form that both creates and corrects. They are grouped in this file
// rather than split three ways because what they have in common — the toggle,
// the redirect back to the list, the way an error is shown — is most of them.

type addressFormData struct {
	Address model.Address
	IsEdit  bool
	Error   string
}

type utilityFormData struct {
	Utility   model.Utility
	Addresses []model.Address
	Tariffs   []model.Tariff
	IsEdit    bool
	Error     string
}

type tariffFormData struct {
	Tariff model.Tariff
	IsEdit bool
	// Locked reports that readings have already been priced by this tariff, so
	// the calculation fields are shown but not editable.
	Locked bool
	Error  string
}

// ── addresses ────────────────────────────────────────────────────────────────

func (a *App) handleAddressNew(w http.ResponseWriter, r *http.Request) {
	a.render(w, "address_form.html", "Адреса", "addresses",
		addressFormData{Address: model.Address{Currency: "UAH", Active: true}})
}

func (a *App) handleAddressEdit(w http.ResponseWriter, r *http.Request) {
	ad, err := a.Store.AddressByID(pathID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.render(w, "address_form.html", "Адреса", "addresses",
		addressFormData{Address: ad, IsEdit: true})
}

func (a *App) handleAddressSave(w http.ResponseWriter, r *http.Request) {
	ad := model.Address{
		ID:        pathID(r),
		Name:      strings.TrimSpace(r.FormValue("name")),
		Comment:   strings.TrimSpace(r.FormValue("comment")),
		Area:      optionalFloat(r.FormValue("area")),
		Currency:  strings.TrimSpace(r.FormValue("currency")),
		SortOrder: formInt(r.FormValue("sort_order")),
		Active:    true,
	}
	if ad.Name == "" {
		a.renderRefError(w, "address_form.html", "Адреса", "addresses",
			addressFormData{Address: ad, IsEdit: ad.ID != 0, Error: "назва не може бути порожньою"})
		return
	}
	if ad.Currency == "" {
		ad.Currency = "UAH"
	}

	var err error
	if ad.ID == 0 {
		_, err = a.Store.CreateAddress(ad)
	} else {
		err = a.Store.UpdateAddress(ad)
	}
	if err != nil {
		a.Logger.Error("web: address save", "err", err)
		a.renderRefError(w, "address_form.html", "Адреса", "addresses",
			addressFormData{Address: ad, IsEdit: ad.ID != 0, Error: "не вдалося зберегти адресу"})
		return
	}
	http.Redirect(w, r, "/meters/addresses", http.StatusSeeOther)
}

// ── utilities ────────────────────────────────────────────────────────────────

func (a *App) handleUtilityNew(w http.ResponseWriter, r *http.Request) {
	d, err := a.utilityForm(model.Utility{Active: true})
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "utility_form.html", "Сервіс", "utilities", d)
}

func (a *App) handleUtilityEdit(w http.ResponseWriter, r *http.Request) {
	u, err := a.Store.UtilityByID(pathID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d, err := a.utilityForm(u)
	if err != nil {
		a.serverError(w, err)
		return
	}
	d.IsEdit = true
	a.render(w, "utility_form.html", "Сервіс", "utilities", d)
}

// utilityForm fills the two dropdowns. Every tariff is offered, not just the
// active ones: a service being corrected may legitimately still point at a
// superseded price.
func (a *App) utilityForm(u model.Utility) (utilityFormData, error) {
	addresses, err := a.Store.AddressesActive()
	if err != nil {
		return utilityFormData{}, err
	}
	all, err := a.Store.TariffsAll()
	if err != nil {
		return utilityFormData{}, err
	}
	tariffs := make([]model.Tariff, 0, len(all))
	for _, t := range all {
		tariffs = append(tariffs, t.Tariff)
	}
	return utilityFormData{Utility: u, Addresses: addresses, Tariffs: tariffs}, nil
}

func (a *App) handleUtilitySave(w http.ResponseWriter, r *http.Request) {
	u := model.Utility{
		ID:        pathID(r),
		AddressID: formInt64(r.FormValue("address_id")),
		Name:      strings.TrimSpace(r.FormValue("name")),
		Icon:      strings.TrimSpace(r.FormValue("icon")),
		// Colour is entered directly. The old app derived it from a category
		// catalogue, which was a second place for it to disagree with itself.
		Color:     strings.TrimSpace(r.FormValue("color")),
		URL:       strings.TrimSpace(r.FormValue("url")),
		Comment:   strings.TrimSpace(r.FormValue("comment")),
		SortOrder: formInt(r.FormValue("sort_order")),
		Active:    true,
	}
	if id := formInt64(r.FormValue("current_tariff_id")); id != 0 {
		u.CurrentTariffID = &id
	}

	fail := func(msg string) {
		d, _ := a.utilityForm(u)
		d.IsEdit = u.ID != 0
		d.Error = msg
		a.renderRefError(w, "utility_form.html", "Сервіс", "utilities", d)
	}
	if u.Name == "" {
		fail("назва не може бути порожньою")
		return
	}
	if u.AddressID == 0 {
		fail("оберіть адресу")
		return
	}

	var err error
	if u.ID == 0 {
		_, err = a.Store.CreateUtility(u)
	} else {
		err = a.Store.UpdateUtility(u)
	}
	if err != nil {
		a.Logger.Error("web: utility save", "err", err)
		fail("не вдалося зберегти сервіс")
		return
	}
	http.Redirect(w, r, "/meters/utilities", http.StatusSeeOther)
}

// ── tariffs ──────────────────────────────────────────────────────────────────

func (a *App) handleTariffNew(w http.ResponseWriter, r *http.Request) {
	a.render(w, "tariff_form.html", "Тариф", "tariffs",
		tariffFormData{Tariff: model.Tariff{Kind: model.KindMeter, Active: true}})
}

func (a *App) handleTariffEdit(w http.ResponseWriter, r *http.Request) {
	t, err := a.Store.TariffByID(pathID(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	used, err := a.Store.TariffUsedInReadings(t.ID)
	if err != nil {
		a.serverError(w, err)
		return
	}
	a.render(w, "tariff_form.html", "Тариф", "tariffs",
		tariffFormData{Tariff: t, IsEdit: true, Locked: used})
}

func (a *App) handleTariffSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не вдалося розібрати форму", http.StatusBadRequest)
		return
	}
	t := model.Tariff{
		ID:      pathID(r),
		Name:    strings.TrimSpace(r.FormValue("name")),
		Kind:    r.FormValue("kind"),
		Unit:    optionalText(r.FormValue("unit")),
		Rate2:   optionalFloat(r.FormValue("rate2")),
		Comment: strings.TrimSpace(r.FormValue("comment")),
		Active:  true,
	}
	if v := optionalFloat(r.FormValue("rate1")); v != nil {
		t.Rate1 = *v
	}
	t.EffectiveFrom = optionalText(r.FormValue("effective_from"))
	t.EffectiveTo = optionalText(r.FormValue("effective_to"))

	locked := false
	if t.ID != 0 {
		var err error
		if locked, err = a.Store.TariffUsedInReadings(t.ID); err != nil {
			a.serverError(w, err)
			return
		}
		// A locked form does not submit its disabled inputs, so a field the
		// request does not carry is read back from the stored row — otherwise
		// saving a rename would blank the rate.
		//
		// Only an absent field, though. A request that does carry one is left
		// exactly as it came, so UpdateTariff can refuse it: filling those in
		// unconditionally would turn a hand-made POST that reprices history
		// into a silent success.
		if locked {
			old, err := a.Store.TariffByID(t.ID)
			if err != nil {
				a.serverError(w, err)
				return
			}
			if !r.Form.Has("kind") {
				t.Kind = old.Kind
			}
			if !r.Form.Has("rate1") {
				t.Rate1 = old.Rate1
			}
			if !r.Form.Has("rate2") {
				t.Rate2 = old.Rate2
			}
			if !r.Form.Has("unit") {
				t.Unit = old.Unit
			}
		}
	}

	fail := func(msg string) {
		a.renderRefError(w, "tariff_form.html", "Тариф", "tariffs",
			tariffFormData{Tariff: t, IsEdit: t.ID != 0, Locked: locked, Error: msg})
	}
	if t.Name == "" {
		fail("назва не може бути порожньою")
		return
	}
	switch t.Kind {
	case model.KindMeter, model.KindMeterZoned, model.KindFlat:
	default:
		fail("невідомий спосіб розрахунку")
		return
	}

	var err error
	if t.ID == 0 {
		_, err = a.Store.CreateTariff(t)
	} else {
		err = a.Store.UpdateTariff(t)
	}
	if errors.Is(err, store.ErrTariffHasHistory) {
		fail(err.Error())
		return
	}
	if err != nil {
		a.Logger.Error("web: tariff save", "err", err)
		fail("не вдалося зберегти тариф")
		return
	}
	http.Redirect(w, r, "/meters/tariffs", http.StatusSeeOther)
}

// ── shared ───────────────────────────────────────────────────────────────────

// handleMeterToggle archives or restores one row. The table comes from the
// route, never from the request body, so the three routes are three fixed
// statements rather than one that takes a table name from a form.
func (a *App) handleMeterToggle(table, back string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.ToggleActive(table, pathID(r)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			a.serverError(w, err)
			return
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
}

func (a *App) renderRefError(w http.ResponseWriter, page, title, active string, data any) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	a.render(w, page, title, active, data)
}

func pathID(r *http.Request) int64 { return formInt64(r.PathValue("id")) }

func formInt64(raw string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return v
}

func formInt(raw string) int { return int(formInt64(raw)) }
