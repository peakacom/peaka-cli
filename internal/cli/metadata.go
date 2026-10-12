package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// metadata is the completion cache: catalogs, schemas, tables and columns of
// the project, fetched through the REST API in the background and on demand.
type metadata struct {
	rt       *Runtime
	project  string
	onChange func() // called when something new arrived (repaints the completion menu)

	mu       sync.Mutex
	catalogs []string            // names, unique
	catIDs   map[string][]string // name -> ids (an internal catalog has several)
	schemas  map[string][]string // catalog -> schema names
	tables   map[string][]string // catalog.schema -> table names
	columns  map[string][]metaColumn
	loading  map[string]bool // keys being fetched
	loaded   map[string]bool
	errs     map[string]string
}

type metaColumn struct {
	Name string
	Type string
}

func newMetadata(rt *Runtime, project string) *metadata {
	return &metadata{
		rt: rt, project: project,
		catIDs: map[string][]string{}, schemas: map[string][]string{}, tables: map[string][]string{},
		columns: map[string][]metaColumn{}, loading: map[string]bool{}, loaded: map[string]bool{}, errs: map[string]string{},
	}
}

// Prefetch loads catalogs, their schemas and the tables of every schema in the background.
func (m *metadata) Prefetch() {
	go func() {
		if err := m.fetchCatalogs(); err != nil {
			return
		}
		for _, c := range m.Catalogs() {
			if err := m.fetchSchemas(c); err != nil {
				continue
			}
			sem := make(chan struct{}, 4)
			var wg sync.WaitGroup
			for _, s := range m.Schemas(c) {
				wg.Add(1)
				go func(s string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					_ = m.fetchTables(c, s)
				}(s)
			}
			wg.Wait()
		}
	}()
}

// Reset forgets everything (\refresh).
func (m *metadata) Reset() {
	m.mu.Lock()
	m.catalogs, m.catIDs = nil, map[string][]string{}
	m.schemas, m.tables, m.columns = map[string][]string{}, map[string][]string{}, map[string][]metaColumn{}
	m.loaded, m.errs = map[string]bool{}, map[string]string{}
	m.mu.Unlock()
	m.Prefetch()
}

func (m *metadata) Catalogs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.loaded["catalogs"] {
		m.kick("catalogs", func() error { return m.fetchCatalogs() })
	}
	return append([]string(nil), m.catalogs...)
}

func (m *metadata) Schemas(catalog string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.loaded["schemas:"+catalog] {
		m.kick("schemas:"+catalog, func() error { return m.fetchSchemas(catalog) })
	}
	return append([]string(nil), m.schemas[catalog]...)
}

func (m *metadata) Tables(catalog, schema string) []string {
	key := catalog + "." + schema
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.loaded["tables:"+key] {
		m.kick("tables:"+key, func() error { return m.fetchTables(catalog, schema) })
	}
	return append([]string(nil), m.tables[key]...)
}

func (m *metadata) Columns(catalog, schema, table string) []metaColumn {
	key := catalog + "." + schema + "." + table
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.loaded["columns:"+key] {
		m.kick("columns:"+key, func() error { return m.fetchColumns(catalog, schema, table) })
	}
	return append([]metaColumn(nil), m.columns[key]...)
}

// kick starts a fetch once; callers hold m.mu.
func (m *metadata) kick(key string, fetch func() error) {
	if m.loading[key] {
		return
	}
	m.loading[key] = true
	go func() {
		err := fetch()
		m.mu.Lock()
		delete(m.loading, key)
		if err != nil {
			m.errs[key] = err.Error()
		}
		m.mu.Unlock()
		if m.onChange != nil {
			m.onChange()
		}
	}()
}

func (m *metadata) get(path string, out any) error {
	client, err := m.rt.api()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.Get(context.Background(), path, nil, &raw); err != nil {
		return err
	}
	return decodeJSON(raw, out)
}

func (m *metadata) fetchCatalogs() error {
	var rs []record
	if err := m.get(fmt.Sprintf("/data/projects/%s/catalogs", m.project), &rs); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.catalogs, m.catIDs = nil, map[string][]string{}
	for _, r := range rs {
		name := str(r["name"])
		if name == "" {
			continue
		}
		if _, seen := m.catIDs[name]; !seen {
			m.catalogs = append(m.catalogs, name)
		}
		m.catIDs[name] = append(m.catIDs[name], str(r["id"]))
	}
	sort.Strings(m.catalogs)
	m.loaded["catalogs"] = true
	return nil
}

// idsOf returns the ids behind a catalog name, loading the catalog list first when needed.
func (m *metadata) idsOf(catalog string) []string {
	m.mu.Lock()
	loaded := m.loaded["catalogs"]
	m.mu.Unlock()
	if !loaded {
		if err := m.fetchCatalogs(); err != nil {
			return nil
		}
	}
	m.mu.Lock()
	ids := append([]string(nil), m.catIDs[catalog]...)
	m.mu.Unlock()
	return ids
}

func (m *metadata) fetchSchemas(catalog string) error {
	ids := m.idsOf(catalog)
	seen := map[string]bool{}
	var names []string
	for _, id := range ids {
		var rs []record
		if err := m.get(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas", m.project, url.PathEscape(id)), &rs); err != nil {
			return err
		}
		for _, r := range rs {
			n := firstNonEmpty(str(r["schemaName"]), str(r["name"]))
			if n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	m.mu.Lock()
	m.schemas[catalog] = names
	m.loaded["schemas:"+catalog] = true
	m.mu.Unlock()
	return nil
}

func (m *metadata) fetchTables(catalog, schema string) error {
	seen := map[string]bool{}
	var names []string
	var lastErr error
	for _, id := range m.idsOf(catalog) {
		var rs []record
		err := m.get(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables", m.project, url.PathEscape(id), url.PathEscape(schema)), &rs)
		if err != nil {
			lastErr = err // the schema may live under the other id of the same name
			continue
		}
		for _, r := range rs {
			n := firstNonEmpty(str(r["tableName"]), str(r["name"]), str(r["table"]), str(r["displayName"]))
			if n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 && lastErr != nil {
		return lastErr
	}
	sort.Strings(names)
	m.mu.Lock()
	m.tables[catalog+"."+schema] = names
	m.loaded["tables:"+catalog+"."+schema] = true
	m.mu.Unlock()
	return nil
}

func (m *metadata) fetchColumns(catalog, schema, table string) error {
	var cols []metaColumn
	var lastErr error
	for _, id := range m.idsOf(catalog) {
		var rs []record
		err := m.get(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables/%s/columns", m.project, url.PathEscape(id), url.PathEscape(schema), url.PathEscape(table)), &rs)
		if err != nil {
			lastErr = err
			continue
		}
		for _, r := range rs {
			n := firstNonEmpty(str(r["columnName"]), str(r["name"]), str(r["column"]))
			if n == "" {
				continue
			}
			cols = append(cols, metaColumn{Name: n, Type: firstNonEmpty(str(r["dataType"]), str(r["type"]), str(r["columnType"]))})
		}
		if len(cols) > 0 {
			break
		}
	}
	if len(cols) == 0 && lastErr != nil {
		return lastErr
	}
	m.mu.Lock()
	m.columns[catalog+"."+schema+"."+table] = cols
	m.loaded["columns:"+catalog+"."+schema+"."+table] = true
	m.mu.Unlock()
	return nil
}

// ResolveTable turns a path as written (possibly unqualified) into catalog, schema, table
// using the session defaults.
func (m *metadata) ResolveTable(path, defCatalog, defSchema string) (c, s, t string, ok bool) {
	parts := strings.Split(strings.ReplaceAll(path, `"`, ""), ".")
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2], true
	case 2:
		if defCatalog != "" {
			return defCatalog, parts[0], parts[1], true
		}
	case 1:
		if defCatalog != "" && defSchema != "" {
			return defCatalog, defSchema, parts[0], true
		}
	}
	return "", "", "", false
}

// Summary tells how much is cached (for \refresh feedback).
func (m *metadata) Summary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	nt := 0
	for _, t := range m.tables {
		nt += len(t)
	}
	return fmt.Sprintf("%d catalogs, %d schemas, %d tables cached", len(m.catalogs), len(m.schemas), nt)
}

// Loading reports whether any fetch is in flight.
func (m *metadata) Loading() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.loading) > 0
}
