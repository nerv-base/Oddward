// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

type diskRecord struct {
	Pool   Pool   `json:"pool"`
	ItemID string `json:"item_id"`
	TimeMS int64  `json:"time_ms"`
	Pity   int    `json:"pity"`
	Luck   int    `json:"luck"`
}

type diskPoolMeta struct {
	Pool         Pool   `json:"pool"`
	M            int    `json:"m"`
	DrawCount    int    `json:"draw_count"`
	RareCount    int    `json:"rare_count"`
	Average      string `json:"average"`
	PlayerOver   string `json:"player_over"`
	RuleMismatch bool   `json:"rule_mismatch"`
}

type diskMeta struct {
	FetchedAt int64          `json:"fetched_at"`
	Pools     []diskPoolMeta `json:"pools"`
	LuckType  int            `json:"luck_type"`
	LuckTitle string         `json:"luck_title"`
}

type oddwardDocument struct {
	Format     string       `json:"format"`
	Version    int          `json:"version"`
	RoleID     string       `json:"role_id"`
	ExportedAt int64        `json:"exported_at"`
	Records    []diskRecord `json:"records"`
	Snapshots  []diskMeta   `json:"snapshots"`
}

type nteRecord struct {
	ItemID string `json:"item_id"`
	TimeMS int64  `json:"time_ms"`
	Pity   int    `json:"pity"`
}

type ntePool struct {
	Records []nteRecord `json:"records"`
}
type nteDocument struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	UID       string           `json:"uid"`
	Source    string           `json:"source"`
	UpdatedAt int64            `json:"updated_at"`
	Pools     map[Pool]ntePool `json:"pools"`
}

func encodeMeta(m SnapshotMeta) diskMeta {
	d := diskMeta{FetchedAt: m.FetchedAt.UnixMilli(), LuckType: m.LuckType, LuckTitle: m.LuckTitle, Pools: []diskPoolMeta{}}
	for _, p := range m.Pools {
		d.Pools = append(d.Pools, diskPoolMeta(p))
	}
	return d
}

func metaKey(m SnapshotMeta) string { b, _ := json.Marshal(encodeMeta(m)); return string(b) }

// ExportOddward produces the full v1 schema shared with on-disk archives.
// Record and snapshot timestamps are canonical UTC milliseconds.
func ExportOddward(a Archive, now time.Time) ([]byte, error) {
	a, err := normalizeArchive(a, false)
	if err != nil {
		return nil, err
	}
	if now.UnixMilli() < 0 {
		return nil, errors.New("export time must not precede the Unix epoch")
	}
	d := oddwardDocument{Format: "oddward-gacha", Version: 1, RoleID: a.RoleID, ExportedAt: now.UnixMilli(), Records: []diskRecord{}, Snapshots: []diskMeta{}}
	for _, r := range a.Records {
		d.Records = append(d.Records, diskRecord{Pool: r.Pool, ItemID: r.ItemID, TimeMS: r.At.UnixMilli(), Pity: r.Pity, Luck: r.Luck})
	}
	for _, m := range a.Snapshots {
		d.Snapshots = append(d.Snapshots, encodeMeta(m))
	}
	return marshalBounded(d)
}

// ExportNTEUID intentionally omits luck and all snapshots. An anomalous pity
// below one cannot be represented by this format and causes an error.
func ExportNTEUID(a Archive, now time.Time) ([]byte, error) {
	a, err := normalizeArchive(a, false)
	if err != nil {
		return nil, err
	}
	if now.Unix() < 0 {
		return nil, errors.New("update time must not precede the Unix epoch")
	}
	d := nteDocument{Format: "nteuid-gacha", Version: 1, UID: a.RoleID, Source: "tajiduo", UpdatedAt: now.Unix(), Pools: map[Pool]ntePool{Limited: {Records: []nteRecord{}}, Permanent: {Records: []nteRecord{}}, Weapon: {Records: []nteRecord{}}}}
	for _, r := range a.Records {
		if r.Pity < 1 {
			return nil, errors.New("NTEUID cannot represent pity below one")
		}
		p := d.Pools[r.Pool]
		p.Records = append(p.Records, nteRecord{ItemID: r.ItemID, TimeMS: r.At.UnixMilli(), Pity: r.Pity})
		d.Pools[r.Pool] = p
	}
	return marshalBounded(d)
}

func marshalBounded(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDocumentBytes {
		return nil, errors.New("gacha document exceeds 8 MiB")
	}
	return data, nil
}

// DecodeImport recognizes only the two supported v1 formats. Every object
// rejects duplicate, unknown, missing, and incorrectly cased field names.
func DecodeImport(data []byte) (Imported, error) {
	if err := checkJSON(data); err != nil {
		return Imported{}, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Imported{}, err
	}
	var format string
	if err := json.Unmarshal(top["format"], &format); err != nil {
		return Imported{}, fmt.Errorf("%w: missing or invalid format", ErrUnknownFormat)
	}
	switch format {
	case "oddward-gacha":
		var d oddwardDocument
		if err := decodeShape(data, &d); err != nil {
			return Imported{}, err
		}
		if d.Version != 1 || d.ExportedAt < 0 {
			return Imported{}, errors.New("unsupported or invalid oddward-gacha version or export time")
		}
		a := Archive{RoleID: d.RoleID}
		for _, r := range d.Records {
			a.Records = append(a.Records, Record{Pool: r.Pool, ItemID: r.ItemID, At: time.UnixMilli(r.TimeMS).UTC(), Pity: r.Pity, Luck: r.Luck})
		}
		if len(d.Snapshots) > MaxSnapshots {
			return Imported{}, errors.New("too many gacha snapshots")
		}
		for _, m := range d.Snapshots {
			meta := SnapshotMeta{FetchedAt: time.UnixMilli(m.FetchedAt).UTC(), LuckType: m.LuckType, LuckTitle: m.LuckTitle}
			for _, p := range m.Pools {
				meta.Pools = append(meta.Pools, PoolMeta(p))
			}
			a.Snapshots = append(a.Snapshots, meta)
		}
		a, err := normalizeArchive(a, false)
		if err != nil {
			return Imported{}, err
		}
		return Imported{Format: format, Archive: a}, nil
	case "nteuid-gacha":
		var d nteDocument
		if err := decodeShape(data, &d); err != nil {
			return Imported{}, err
		}
		if d.Version != 1 || d.Source != "tajiduo" || d.UpdatedAt < 0 {
			return Imported{}, errors.New("unsupported or invalid nteuid-gacha schema")
		}
		a := Archive{RoleID: d.UID}
		for pool, p := range d.Pools {
			if err := ValidatePool(pool); err != nil {
				return Imported{}, err
			}
			for _, r := range p.Records {
				if r.Pity < 1 {
					return Imported{}, errors.New("NTEUID pity must be at least one")
				}
				a.Records = append(a.Records, Record{Pool: pool, ItemID: r.ItemID, At: time.UnixMilli(r.TimeMS).UTC(), Pity: r.Pity, Luck: LuckUnknown})
			}
		}
		a, err := normalizeArchive(a, false)
		if err != nil {
			return Imported{}, err
		}
		return Imported{Format: format, Archive: a}, nil
	default:
		return Imported{}, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
}

// checkJSON checks structure before typed decoding, including duplicate names
// at every nesting level. A depth cap bounds recursion on untrusted imports.
func checkJSON(data []byte) error {
	if len(data) > MaxDocumentBytes {
		return errors.New("gacha document exceeds 8 MiB")
	}
	if !utf8.Valid(data) {
		return errors.New("gacha document must be UTF-8")
	}
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("gacha document must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := scanJSON(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing gacha JSON data")
	}
	return nil
}

func scanJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("gacha JSON nesting exceeds limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid JSON field")
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON field %q", name)
			}
			seen[name] = true
			if err := scanJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := scanJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}

func decodeShape(data []byte, out any) error {
	if err := validateShape(data, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

// validateShape requires all schema fields, rejects null containers/scalars,
// and avoids encoding/json's case-insensitive struct-field name matching.
func validateShape(raw json.RawMessage, t reflect.Type) error {
	data := bytes.TrimSpace(raw)
	if bytes.Equal(data, []byte("null")) {
		return errors.New("null is not a gacha schema value")
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		if len(fields) != t.NumField() {
			return errors.New("missing or unknown gacha fields")
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			value, ok := fields[name]
			if !ok {
				return fmt.Errorf("missing gacha field %q", name)
			}
			if err := validateShape(value, field.Type); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateShape(value, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateShape(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
