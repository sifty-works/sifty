package candidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/server"
)

// --- request and response types ---

// MePatchReq is the body for PATCH /api/me. Each field is a raw JSON section;
// nil means the section was not sent and must not be changed.
type MePatchReq struct {
	DisplayName        *string              `json:"displayName,omitempty"`
	LegalName          *string              `json:"legalName,omitempty"`
	Email              *string              `json:"email,omitempty"`
	Phone              *string              `json:"phone,omitempty"`
	LinkedIn           *string              `json:"linkedin,omitempty"`
	GitHub             *string              `json:"github,omitempty"`
	X                  *string              `json:"x,omitempty"`
	Website            *string              `json:"website,omitempty"`
	Skills             *[]Skill             `json:"skills,omitempty"`
	AbsentSkills       *[]AbsentSkill       `json:"absentSkills,omitempty"`
	ConfirmedFacts     *[]ConfirmedFact     `json:"confirmedFacts,omitempty"`
	Experience         *[]Experience        `json:"experience,omitempty"`
	Languages          *[]Language          `json:"languages,omitempty"`
	Availability       *Availability        `json:"availability,omitempty"`
	CurrentLocation    *Location            `json:"currentLocation,omitempty"`
	WorkAuthorizations *[]WorkAuthorization `json:"workAuthorizations,omitempty"`
	Demographics       *Demographics        `json:"demographics,omitempty"`
}

// ProfileListItem is one entry in GET /api/profiles.
type ProfileListItem struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// ProfileListResp is the response for GET /api/profiles.
type ProfileListResp struct {
	Profiles []ProfileListItem `json:"profiles"`
}

// InheritedSource describes from which profile a field is inherited.
type InheritedSource struct {
	ProfileID   int64  `json:"profileId"`
	ProfileName string `json:"profileName"`
}

// ProfileDetailResp is the response for GET /api/profiles/{id}.
type ProfileDetailResp struct {
	ID        int64                      `json:"id"`
	Name      string                     `json:"name"`
	Active    bool                       `json:"active"`
	Own       ProfileData                `json:"own"`
	Effective ProfileData                `json:"effective"`
	Inherited map[string]InheritedSource `json:"inherited,omitempty"`
	Headline  string                     `json:"headline"`
}

// CreateProfileReq is the body for POST /api/profiles.
type CreateProfileReq struct {
	Name string `json:"name"`
}

// ProfilePatchReq is the body for PATCH /api/profiles/{id}.
// Fields that are nil were not sent and must not be changed.
type ProfilePatchReq struct {
	Name               *string       `json:"name,omitempty"`
	Headline           *string       `json:"headline,omitempty"`
	TargetRoles        *[]string     `json:"targetRoles,omitempty"`
	Keywords           *[]string     `json:"keywords,omitempty"`
	AllOf              *[]string     `json:"allOf,omitempty"`
	TargetIndustries   *[]string     `json:"targetIndustries,omitempty"`
	Compensation       *Compensation `json:"compensation,omitempty"`
	Engagement         *[]string     `json:"engagement,omitempty"`
	Hours              *Hours        `json:"hours,omitempty"`
	WorkModes          *[]string     `json:"workModes,omitempty"`
	PreferredLocations *[]Location   `json:"preferredLocations,omitempty"`
	Relocation         *string       `json:"relocation,omitempty"`
	TimeZones          *[]string     `json:"timeZones,omitempty"`
	OtherRequirements  *string       `json:"otherRequirements,omitempty"`
}

// SetActiveReq is the body for PUT /api/active-profile.
type SetActiveReq struct {
	ID int64 `json:"id"`
}

// QAItem is one entry in the Questions & Answers list.
type QAItem struct {
	ID        int64  `json:"id"`
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// QAListResp is the response for GET /api/qa.
type QAListResp struct {
	Items []QAItem `json:"items"`
}

// QACreateReq is the body for POST /api/qa.
type QACreateReq struct {
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
}

// QAPatchReq is the body for PATCH /api/qa.
type QAPatchReq struct {
	ID       int64   `json:"id"`
	Question *string `json:"question,omitempty"`
	Answer   *string `json:"answer,omitempty"`
}

// QADeleteReq is the body for DELETE /api/qa.
type QADeleteReq struct {
	ID int64 `json:"id"`
}

// --- helpers ---

// loadMeData reads the single Me row from the database.
func loadMeData(ctx context.Context, database *db.DB) (MeData, error) {
	var dataJSON string
	err := database.QueryRowContext(ctx, `SELECT data FROM me WHERE id=1`).Scan(&dataJSON)
	if err == sql.ErrNoRows {
		return MeData{}, nil
	}
	if err != nil {
		return MeData{}, fmt.Errorf("candidate: load me: %w", err)
	}
	var me MeData
	if err := json.Unmarshal([]byte(dataJSON), &me); err != nil {
		return MeData{}, fmt.Errorf("candidate: decode me: %w", err)
	}
	return me, nil
}

// saveMeData writes the Me record to the database.
func saveMeData(ctx context.Context, database *db.DB, me MeData) error {
	NormalizeMeData(&me)
	data, err := json.Marshal(me)
	if err != nil {
		return fmt.Errorf("candidate: encode me: %w", err)
	}
	_, err = database.ExecContext(ctx, `INSERT OR REPLACE INTO me (id, data) VALUES (1, ?)`, string(data))
	if err != nil {
		return fmt.Errorf("candidate: save me: %w", err)
	}
	return nil
}

// activeProfileID returns the id of the active profile from settings.
// Falls back to the profile with the lowest id when not set.
func activeProfileID(ctx context.Context, database *db.DB) (int64, error) {
	var v string
	err := database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='active_profile'`).Scan(&v)
	if err == nil {
		id, parseErr := strconv.ParseInt(v, 10, 64)
		if parseErr == nil && id > 0 {
			// Verify it still exists.
			var check int64
			if chkErr := database.QueryRowContext(ctx, `SELECT id FROM profile WHERE id=?`, id).Scan(&check); chkErr == nil {
				return check, nil
			}
		}
	}
	// Fall back to the first profile.
	var id int64
	if dbErr := database.QueryRowContext(ctx, `SELECT id FROM profile ORDER BY id LIMIT 1`).Scan(&id); dbErr != nil {
		if dbErr == sql.ErrNoRows {
			return 0, apperr.NotFound{Message: "Profile not found."}
		}
		return 0, fmt.Errorf("candidate: active profile: %w", dbErr)
	}
	return id, nil
}

// setActiveProfile writes the active profile setting.
func setActiveProfile(ctx context.Context, database *db.DB, id int64) error {
	_, err := database.ExecContext(ctx,
		`INSERT OR REPLACE INTO settings (key, value) VALUES ('active_profile', ?)`,
		strconv.FormatInt(id, 10))
	if err != nil {
		return fmt.Errorf("candidate: set active profile: %w", err)
	}
	return nil
}

// loadProfileRow reads one profile row by id. Returns NotFound when absent.
func loadProfileRow(ctx context.Context, database *db.DB, id int64) (ProfileData, error) {
	var dataJSON string
	err := database.QueryRowContext(ctx, `SELECT data FROM profile WHERE id=?`, id).Scan(&dataJSON)
	if err == sql.ErrNoRows {
		return ProfileData{}, apperr.NotFound{Message: "Profile not found."}
	}
	if err != nil {
		return ProfileData{}, fmt.Errorf("candidate: load profile %d: %w", id, err)
	}
	var pd ProfileData
	if err := json.Unmarshal([]byte(dataJSON), &pd); err != nil {
		return ProfileData{}, fmt.Errorf("candidate: decode profile %d: %w", id, err)
	}
	return pd, nil
}

// saveProfileRow writes one profile row.
func saveProfileRow(ctx context.Context, database *db.DB, id int64, pd ProfileData) error {
	NormalizeProfileData(&pd)
	data, err := json.Marshal(pd)
	if err != nil {
		return fmt.Errorf("candidate: encode profile %d: %w", id, err)
	}
	_, err = database.ExecContext(ctx, `UPDATE profile SET data=? WHERE id=?`, string(data), id)
	if err != nil {
		return fmt.Errorf("candidate: save profile %d: %w", id, err)
	}
	return nil
}

// firstProfileID returns the id of the first (lowest-id) profile.
func firstProfileID(ctx context.Context, database *db.DB) (int64, error) {
	var id int64
	err := database.QueryRowContext(ctx, `SELECT id FROM profile ORDER BY id LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, apperr.NotFound{Message: "Profile not found."}
	}
	if err != nil {
		return 0, fmt.Errorf("candidate: first profile: %w", err)
	}
	return id, nil
}

// buildHeadline returns the headline to show for a profile.
// When the profile has an explicit headline it is returned; otherwise a line
// "role · company · city, country" is built from the first experience entry
// and the currentLocation from Me data, or "Candidate profile" when nothing
// is available.
func buildHeadline(pd ProfileData, me MeData) string {
	if strings.TrimSpace(pd.Headline) != "" {
		return pd.Headline
	}
	// Find the current or first experience role.
	var exp *Experience
	for i := range me.Experience {
		if me.Experience[i].Current {
			exp = &me.Experience[i]
			break
		}
	}
	if exp == nil && len(me.Experience) > 0 {
		exp = &me.Experience[0]
	}
	var parts []string
	if exp != nil {
		if t := strings.TrimSpace(exp.Title); t != "" {
			parts = append(parts, t)
		}
		if c := strings.TrimSpace(exp.Company); c != "" {
			parts = append(parts, c)
		}
	}
	if me.CurrentLocation != nil {
		city := strings.TrimSpace(me.CurrentLocation.City)
		country := strings.TrimSpace(me.CurrentLocation.Country)
		if city != "" && country != "" {
			parts = append(parts, city+", "+country)
		} else if country != "" {
			parts = append(parts, country)
		}
	}
	if len(parts) == 0 {
		return "Candidate profile"
	}
	return strings.Join(parts, " · ")
}

// jsonEqual reports whether a and b have the same JSON representation.
func jsonEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// clearInheritedUnchanged nils out inheritable patch fields that equal the first
// profile's value when own has no value for them. This preserves the
// "saved unchanged stays inherited" rule for non-first profiles.
func clearInheritedUnchanged(patch *ProfilePatchReq, own, first ProfileData) {
	if patch.Compensation != nil && own.Compensation == nil && jsonEqual(patch.Compensation, first.Compensation) {
		patch.Compensation = nil
	}
	if patch.Engagement != nil && len(own.Engagement) == 0 && jsonEqual(*patch.Engagement, first.Engagement) {
		patch.Engagement = nil
	}
	if patch.Hours != nil && own.Hours == nil && jsonEqual(patch.Hours, first.Hours) {
		patch.Hours = nil
	}
	if patch.WorkModes != nil && len(own.WorkModes) == 0 && jsonEqual(*patch.WorkModes, first.WorkModes) {
		patch.WorkModes = nil
	}
	if patch.PreferredLocations != nil && len(own.PreferredLocations) == 0 && jsonEqual(*patch.PreferredLocations, first.PreferredLocations) {
		patch.PreferredLocations = nil
	}
	if patch.TargetIndustries != nil && len(own.TargetIndustries) == 0 && jsonEqual(*patch.TargetIndustries, first.TargetIndustries) {
		patch.TargetIndustries = nil
	}
}

// mergePatch overlays the non-nil fields of src onto dst and returns the result.
// src must be a struct whose pointer fields carry omitempty; absent (nil) fields
// are skipped. String fields at the top level of the result are trimmed.
func mergePatch[D any](dst D, src any) (D, error) {
	base, _ := json.Marshal(dst)
	over, _ := json.Marshal(src)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return dst, err
	}
	var p map[string]json.RawMessage
	json.Unmarshal(over, &p) //nolint:errcheck
	for k, v := range p {
		m[k] = v
	}
	merged, _ := json.Marshal(m)
	var result D
	if err := json.Unmarshal(merged, &result); err != nil {
		return dst, fmt.Errorf("candidate: merge patch: %w", err)
	}
	trimStrings(&result)
	return result, nil
}

// trimStrings trims leading and trailing whitespace from every exported string
// field at the top level of the struct pointed to by v.
func trimStrings[T any](v *T) {
	rv := reflect.ValueOf(v).Elem()
	for i := 0; i < rv.NumField(); i++ {
		if f := rv.Field(i); f.CanSet() && f.Kind() == reflect.String {
			f.SetString(strings.TrimSpace(f.String()))
		}
	}
}

// photoPath returns the path where the profile photo is stored.
func photoPath(filesDir string) string {
	return filepath.Join(filesDir, "me_photo")
}

// RegisterRoutes registers all candidate routes on mux.
// filesDir is the path to the data files directory (e.g. <data>/files/); used for photo storage.
// When filesDir is empty, photo endpoints are registered but always return an error.
func RegisterRoutes(mux *http.ServeMux, database *db.DB, filesDir string, logger *slog.Logger) {
	server.Handle(mux, "GET /api/me", logger, func(r *http.Request, _ struct{}) (MeData, error) {
		return loadMeData(r.Context(), database)
	})

	server.Handle(mux, "PATCH /api/me", logger, func(r *http.Request, patch MePatchReq) (MeData, error) {
		ctx := r.Context()
		me, err := loadMeData(ctx, database)
		if err != nil {
			return MeData{}, err
		}
		updated, err := mergePatch(me, patch)
		if err != nil {
			return MeData{}, err
		}
		if errs := ValidateMeData(updated); len(errs) > 0 {
			return MeData{}, apperr.Invalid{Message: strings.Join(errs, "; ")}
		}
		if err := saveMeData(ctx, database, updated); err != nil {
			return MeData{}, err
		}
		return updated, nil
	})

	server.Handle(mux, "GET /api/profiles", logger, func(r *http.Request, _ struct{}) (ProfileListResp, error) {
		return listProfiles(r.Context(), database)
	})

	server.Handle(mux, "GET /api/profiles/{id}", logger, func(r *http.Request, _ struct{}) (ProfileDetailResp, error) {
		ctx := r.Context()
		id, err := parseProfileID(r)
		if err != nil {
			return ProfileDetailResp{}, err
		}
		own, err := loadProfileRow(ctx, database, id)
		if err != nil {
			return ProfileDetailResp{}, err
		}
		return loadProfileDetail(ctx, database, id, own)
	})

	server.Handle(mux, "POST /api/profiles", logger, func(r *http.Request, req CreateProfileReq) (ProfileDetailResp, error) {
		ctx := r.Context()
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return ProfileDetailResp{}, apperr.Invalid{Message: "Name must not be empty."}
		}
		pd := ProfileData{Name: name}
		data, err := json.Marshal(pd)
		if err != nil {
			return ProfileDetailResp{}, fmt.Errorf("candidate: encode new profile: %w", err)
		}
		res, err := database.ExecContext(ctx, `INSERT INTO profile (data) VALUES (?)`, string(data))
		if err != nil {
			return ProfileDetailResp{}, fmt.Errorf("candidate: create profile: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return ProfileDetailResp{}, fmt.Errorf("candidate: new profile id: %w", err)
		}
		// Make the new profile active.
		if err := setActiveProfile(ctx, database, newID); err != nil {
			return ProfileDetailResp{}, err
		}
		return loadProfileDetail(ctx, database, newID, pd)
	})

	server.Handle(mux, "PATCH /api/profiles/{id}", logger, func(r *http.Request, patch ProfilePatchReq) (ProfileDetailResp, error) {
		ctx := r.Context()
		id, err := parseProfileID(r)
		if err != nil {
			return ProfileDetailResp{}, err
		}
		own, err := loadProfileRow(ctx, database, id)
		if err != nil {
			return ProfileDetailResp{}, err
		}
		// Reject empty name explicitly sent.
		if patch.Name != nil && strings.TrimSpace(*patch.Name) == "" {
			patch.Name = nil // ignore empty name silently per document
		}
		// For non-first profiles: sending an inherited value unchanged keeps it inherited.
		firstID, _ := firstProfileID(ctx, database)
		if firstID != id {
			if firstPD, fErr := loadProfileRow(ctx, database, firstID); fErr == nil {
				clearInheritedUnchanged(&patch, own, firstPD)
			}
		}
		updated, err := mergePatch(own, patch)
		if err != nil {
			return ProfileDetailResp{}, err
		}
		if errs := ValidateProfileData(updated); len(errs) > 0 {
			return ProfileDetailResp{}, apperr.Invalid{Message: strings.Join(errs, "; ")}
		}
		if err := saveProfileRow(ctx, database, id, updated); err != nil {
			return ProfileDetailResp{}, err
		}
		return loadProfileDetail(ctx, database, id, updated)
	})

	server.Handle(mux, "DELETE /api/profiles/{id}", logger, func(r *http.Request, _ struct{}) (ProfileListResp, error) {
		ctx := r.Context()
		id, err := parseProfileID(r)
		if err != nil {
			return ProfileListResp{}, err
		}
		firstID, err := firstProfileID(ctx, database)
		if err != nil {
			return ProfileListResp{}, err
		}
		if id == firstID {
			return ProfileListResp{}, apperr.Invalid{Message: "The first profile cannot be deleted."}
		}
		// Verify the profile exists.
		if _, err := loadProfileRow(ctx, database, id); err != nil {
			return ProfileListResp{}, err
		}
		// Remove decisions, profile exclusions, and the profile itself.
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return ProfileListResp{}, fmt.Errorf("candidate: delete profile tx: %w", err)
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.ExecContext(ctx, `DELETE FROM exclusion WHERE profile_id=?`, id); err != nil {
			return ProfileListResp{}, fmt.Errorf("candidate: delete profile exclusions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM decision WHERE profile=?`, id); err != nil {
			return ProfileListResp{}, fmt.Errorf("candidate: delete profile decisions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM profile WHERE id=?`, id); err != nil {
			return ProfileListResp{}, fmt.Errorf("candidate: delete profile: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ProfileListResp{}, fmt.Errorf("candidate: delete profile commit: %w", err)
		}
		// If this was the active profile, switch to the first.
		activeID, _ := activeProfileID(ctx, database)
		if activeID == 0 || activeID == id {
			setActiveProfile(ctx, database, firstID) //nolint:errcheck
		}
		// Return updated list.
		return listProfiles(ctx, database)
	})

	server.Handle(mux, "PUT /api/active-profile", logger, func(r *http.Request, req SetActiveReq) (ProfileListResp, error) {
		ctx := r.Context()
		if req.ID == 0 {
			return ProfileListResp{}, apperr.Invalid{Message: "ID is required."}
		}
		// Verify it exists.
		if _, err := loadProfileRow(ctx, database, req.ID); err != nil {
			return ProfileListResp{}, err
		}
		if err := setActiveProfile(ctx, database, req.ID); err != nil {
			return ProfileListResp{}, err
		}
		return listProfiles(ctx, database)
	})

	// --- photo ---

	// PUT /api/me/photo accepts a raw png, jpg or webp image body (at most 2 MiB).
	// The server.BinaryUploadPaths option must include "/api/me/photo" for this route
	// to bypass the JSON Content-Type check and the 1 MB body limit.
	server.HandleRaw(mux, "PUT /api/me/photo", func(w http.ResponseWriter, r *http.Request) {
		if filesDir == "" {
			server.MapError(w, fmt.Errorf("candidate: no files dir configured"), logger)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			server.MapError(w, apperr.Invalid{Message: "Could not read the upload."}, logger)
			return
		}
		if len(body) == 0 {
			server.MapError(w, apperr.Invalid{Message: "The photo is empty."}, logger)
			return
		}
		ct := http.DetectContentType(body)
		if ct != "image/png" && ct != "image/jpeg" && ct != "image/webp" {
			server.MapError(w, apperr.Invalid{Message: "The photo must be a PNG, JPEG or WebP image."}, logger)
			return
		}
		if err := os.MkdirAll(filesDir, 0700); err != nil {
			server.MapError(w, fmt.Errorf("candidate: photo dir: %w", err), logger)
			return
		}
		if err := os.WriteFile(photoPath(filesDir), body, 0600); err != nil {
			server.MapError(w, fmt.Errorf("candidate: save photo: %w", err), logger)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	server.Handle(mux, "DELETE /api/me/photo", logger, func(r *http.Request, _ struct{}) (struct{}, error) {
		if filesDir == "" {
			return struct{}{}, fmt.Errorf("candidate: no files dir configured")
		}
		path := photoPath(filesDir)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return struct{}{}, fmt.Errorf("candidate: delete photo: %w", err)
		}
		return struct{}{}, nil
	})

	// GET /api/me/photo serves the raw image bytes with the detected content type.
	server.HandleRaw(mux, "GET /api/me/photo", func(w http.ResponseWriter, r *http.Request) {
		if filesDir == "" {
			server.MapError(w, apperr.NotFound{Message: "No photo set."}, logger)
			return
		}
		body, err := os.ReadFile(photoPath(filesDir))
		if os.IsNotExist(err) {
			server.MapError(w, apperr.NotFound{Message: "No photo set."}, logger)
			return
		}
		if err != nil {
			server.MapError(w, fmt.Errorf("candidate: read photo: %w", err), logger)
			return
		}
		ct := http.DetectContentType(body)
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		w.Write(body) //nolint:errcheck
	})

	// --- Q&A ---

	server.Handle(mux, "GET /api/qa", logger, func(r *http.Request, _ struct{}) (QAListResp, error) {
		ctx := r.Context()
		rows, err := database.QueryContext(ctx,
			`SELECT id, question, answer, created_at, updated_at FROM qa ORDER BY id`)
		if err != nil {
			return QAListResp{}, fmt.Errorf("candidate: list qa: %w", err)
		}
		defer rows.Close()
		var items []QAItem
		for rows.Next() {
			var it QAItem
			if err := rows.Scan(&it.ID, &it.Question, &it.Answer, &it.CreatedAt, &it.UpdatedAt); err != nil {
				return QAListResp{}, fmt.Errorf("candidate: scan qa: %w", err)
			}
			items = append(items, it)
		}
		if err := rows.Err(); err != nil {
			return QAListResp{}, fmt.Errorf("candidate: list qa: %w", err)
		}
		if items == nil {
			items = []QAItem{}
		}
		return QAListResp{Items: items}, nil
	})

	server.Handle(mux, "POST /api/qa", logger, func(r *http.Request, req QACreateReq) (QAItem, error) {
		ctx := r.Context()
		q := strings.TrimSpace(req.Question)
		if q == "" {
			return QAItem{}, apperr.Invalid{Message: "Question must not be empty."}
		}
		a := strings.TrimSpace(req.Answer)
		now := time.Now().UTC().Format(time.RFC3339)
		res, err := database.ExecContext(ctx,
			`INSERT INTO qa (question, answer, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			q, a, now, now)
		if err != nil {
			return QAItem{}, fmt.Errorf("candidate: create qa: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return QAItem{}, fmt.Errorf("candidate: qa id: %w", err)
		}
		return QAItem{ID: id, Question: q, Answer: a, CreatedAt: now, UpdatedAt: now}, nil
	})

	server.Handle(mux, "PATCH /api/qa", logger, func(r *http.Request, req QAPatchReq) (QAItem, error) {
		ctx := r.Context()
		if req.ID == 0 {
			return QAItem{}, apperr.Invalid{Message: "ID is required."}
		}
		var current QAItem
		err := database.QueryRowContext(ctx,
			`SELECT id, question, answer, created_at, updated_at FROM qa WHERE id=?`, req.ID).
			Scan(&current.ID, &current.Question, &current.Answer, &current.CreatedAt, &current.UpdatedAt)
		if err == sql.ErrNoRows {
			return QAItem{}, apperr.NotFound{Message: "Q&A entry not found."}
		}
		if err != nil {
			return QAItem{}, fmt.Errorf("candidate: load qa: %w", err)
		}
		if req.Question != nil {
			current.Question = strings.TrimSpace(*req.Question)
		}
		if req.Answer != nil {
			current.Answer = strings.TrimSpace(*req.Answer)
		}
		if strings.TrimSpace(current.Question) == "" {
			return QAItem{}, apperr.Invalid{Message: "Question must not be empty."}
		}
		now := time.Now().UTC().Format(time.RFC3339)
		current.UpdatedAt = now
		_, err = database.ExecContext(ctx,
			`UPDATE qa SET question=?, answer=?, updated_at=? WHERE id=?`,
			current.Question, current.Answer, now, current.ID)
		if err != nil {
			return QAItem{}, fmt.Errorf("candidate: update qa: %w", err)
		}
		return current, nil
	})

	server.Handle(mux, "DELETE /api/qa", logger, func(r *http.Request, req QADeleteReq) (struct{}, error) {
		ctx := r.Context()
		if req.ID == 0 {
			return struct{}{}, apperr.Invalid{Message: "ID is required."}
		}
		res, err := database.ExecContext(ctx, `DELETE FROM qa WHERE id=?`, req.ID)
		if err != nil {
			return struct{}{}, fmt.Errorf("candidate: delete qa: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return struct{}{}, apperr.NotFound{Message: "Q&A entry not found."}
		}
		return struct{}{}, nil
	})

	// --- reference ---

	server.Handle(mux, "GET /api/reference", logger, func(r *http.Request, _ struct{}) (ReferenceResp, error) {
		return referenceData, nil
	})
}

// listProfiles reads all profiles and returns a ProfileListResp.
func listProfiles(ctx context.Context, database *db.DB) (ProfileListResp, error) {
	activeID, _ := activeProfileID(ctx, database)
	rows, err := database.QueryContext(ctx, `SELECT id, data FROM profile ORDER BY id`)
	if err != nil {
		return ProfileListResp{}, fmt.Errorf("candidate: list profiles: %w", err)
	}
	defer rows.Close()
	var items []ProfileListItem
	for rows.Next() {
		var id int64
		var dataJSON string
		if err := rows.Scan(&id, &dataJSON); err != nil {
			return ProfileListResp{}, err
		}
		var pd ProfileData
		json.Unmarshal([]byte(dataJSON), &pd) //nolint:errcheck
		items = append(items, ProfileListItem{ID: id, Name: pd.Name, Active: id == activeID})
	}
	if err := rows.Err(); err != nil {
		return ProfileListResp{}, fmt.Errorf("candidate: list profiles: %w", err)
	}
	if items == nil {
		items = []ProfileListItem{}
	}
	return ProfileListResp{Profiles: items}, nil
}

// loadProfileDetail builds a ProfileDetailResp for profile id with own data own.
func loadProfileDetail(ctx context.Context, database *db.DB, id int64, own ProfileData) (ProfileDetailResp, error) {
	firstID, err := firstProfileID(ctx, database)
	if err != nil {
		return ProfileDetailResp{}, err
	}
	firstPD, err := loadProfileRow(ctx, database, firstID)
	if err != nil {
		return ProfileDetailResp{}, err
	}
	activeID, _ := activeProfileID(ctx, database)
	me, _ := loadMeData(ctx, database)
	effective := own
	var inherited map[string]InheritedSource
	if id != firstID {
		effective = Effective(own, firstPD)
		for _, f := range InheritedFields(own, firstPD) {
			if inherited == nil {
				inherited = make(map[string]InheritedSource)
			}
			inherited[f] = InheritedSource{ProfileID: firstID, ProfileName: firstPD.Name}
		}
	}
	return ProfileDetailResp{
		ID:        id,
		Name:      own.Name,
		Active:    id == activeID,
		Own:       own,
		Effective: effective,
		Inherited: inherited,
		Headline:  buildHeadline(own, me),
	}, nil
}

// parseProfileID parses the {id} path value from the request.
func parseProfileID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, apperr.NotFound{Message: "Profile not found."}
	}
	return id, nil
}

// ActiveProfileID is the exported version of activeProfileID for use in inbox.
func ActiveProfileID(ctx context.Context, database *db.DB) (int64, error) {
	return activeProfileID(ctx, database)
}
