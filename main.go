// Copyright ©2025 Dan Kortschak. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The fkm program initialises and populates a keymapp sqlite3 database
// with metadata and layout information to allow ZSA keymapp to be used in an
// environment where network access by unauditable software is not allowed.
//
// Use OpenSnitch or Little Snitch to ensure it is not communicating with
// the world.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	addr := flag.String("layout", "", "link to configure.zsa.io page for layout (required)")
	dbPath := flag.String("path", "~/.config/.keymapp/keymapp.sqlite3", "path to kaymapp config database")
	mkDir := flag.Bool("mkdir", true, "create config directory")
	flag.Parse()
	if *addr == "" {
		flag.Usage()
		os.Exit(2)
	}

	id, rev, err := revision(*addr)
	if err != nil {
		log.Fatalf("failed to collect revision data: %v", err)
	}

	var ok bool
	*dbPath, ok = strings.CutPrefix(*dbPath, "~/")
	if ok {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("unable to get home directory: %v", err)
		}
		*dbPath = filepath.Join(home, *dbPath)
	}
	if *mkDir {
		err = os.MkdirAll(filepath.Dir(*dbPath), 0o750)
		if err != nil {
			log.Fatalf("unable to get home directory: %v", err)
		}
	}
	db, err := openDB(*dbPath)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// I know. ಠ_ಠ
	row := db.QueryRow(`SELECT count(*) FROM metadata`)
	var n int
	err = row.Scan(&n)
	if n == 0 {
		meta, err := metadata()
		if err != nil {
			log.Fatalf("failed to collect metadata: %v", err)
		}
		_, err = db.Exec(`INSERT INTO metadata (data) VALUES (?)`, meta)
		if err != nil {
			log.Fatal(err)
		}
	}

	_, err = db.Exec(`INSERT INTO revision (revisionId, data) VALUES (?, ?) ON CONFLICT DO UPDATE SET data=?`, id, rev, rev)
	if err != nil {
		log.Fatal(err)
	}
}

func metadata() ([]byte, error) {
	resp, err := http.Get("https://configure.zsa.io/metadata.json")
	if err != nil {
		fmt.Errorf("failed to get metadata: %w", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, err = io.Copy(&buf, resp.Body)
	if err != nil {
		fmt.Errorf("failed to read metadata: %w", err)
	}
	return buf.Bytes(), nil
}

func revision(addr string) (string, []byte, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse URL: %v", err)
	}
	p := strings.Split(strings.TrimLeft(u.Path, "/"), "/")
	if len(p) < 4 {
		return "", nil, fmt.Errorf("invalid config page: %v", addr)
	}
	geom := p[0]
	layout := p[2]
	rev := p[3]

	var query = struct {
		OperationName string            `json:"operationName"`
		Variable      map[string]string `json:"variables"`
		Query         string            `json:"query"`
	}{
		OperationName: "getLayout",
		Variable: map[string]string{
			"hashId":     layout,
			"geometry":   geom,
			"revisionId": rev,
		},
		Query: `
query getLayout($hashId: String!, $revisionId: String!, $geometry: String) {
	layout(hashId: $hashId, geometry: $geometry, revisionId: $revisionId) {
		...LayoutData
	}
}
fragment LayoutData on Layout {
	privacy
	geometry
	hashId
	parent {
		hashId
	}
	tags {
		id
		hashId
		name
	}
	title
	user {
		annotation
		annotationPublic
		name
		hashId
		pictureUrl
	}
	isDefault
	revision {
		...RevisionData
	}
	lastRevisionCompiled
	isLatestRevision
}
fragment RevisionData on Revision {
	createdAt
	hashId
	model
	title
	config
	swatch
	qmkVersion
	qmkUptodate
	hasDeletedLayers
	md5
	combos {
		keyIndices
		layerIdx
		name
		trigger
	}
	tour {
		...TourData
	}
	layers {
		builtIn
		hashId
		keys
		position
		title
		color
		prevHashId
	}
}
fragment TourData on Tour {
	hashId url steps: tourSteps {
		hashId intro outro position content keyIndex layer {
			hashId position
		}
	}
}
`,
	}
	b, err := json.Marshal(query)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal query: %v", err)
	}

	resp, err := http.Post("https://oryx.zsa.io/graphql", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", nil, fmt.Errorf("failed to get revision data: %w", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, err = io.Copy(&buf, resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read revision data: %w", err)
	}
	var body struct {
		Data json.RawMessage `json:"Data"`
	}
	err = json.Unmarshal(buf.Bytes(), &body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse revision data: %w", err)
	}

	var revID struct {
		Layout struct {
			Revision struct {
				HashID string `json:"hashId"`
			} `json:"revision"`
		} `json:"layout"`
	}
	err = json.Unmarshal(body.Data, &revID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse revision ID: %w", err)
	}
	return revID.Layout.Revision.HashID, body.Data, nil
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(schema)
	if err != nil {
		return nil, err
	}
	for _, kv := range defaultConfig {
		// I know. ¯\_(ツ)_/¯
		row := db.QueryRow(`SELECT count(*) FROM config WHERE key=?`, kv.key)
		var n int
		err := row.Scan(&n)
		if err != nil {
			return nil, err
		}
		if n != 0 {
			continue
		}
		_, err = db.Exec(`INSERT INTO config (key, value) VALUES (?, ?)`, kv.key, kv.val)
		if err != nil {
			return nil, err
		}
	}
	return db, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS "config" (
            key TEXT,
            value TEXT
        );
CREATE TABLE IF NOT EXISTS "metadata" (
            data BLOB
        );
CREATE TABLE IF NOT EXISTS "heatmap" (
            revisionId TEXT NOT NULL UNIQUE,
            enabled boolean DEFAULT 0,
            data BLOB DEFAULT NULL
        );
CREATE TABLE IF NOT EXISTS "revision" (
            revisionId TEXT NOT NULL UNIQUE,
            data BLOB DEFAULT NULL
        );
CREATE TABLE IF NOT EXISTS "smart_layer" (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            app TEXT NOT NULL,
            layer INTEGER NOT NULL,
            layoutId TEXT NOT NULL,
            revisionId TEXT NOT NULL
        );
CREATE TABLE IF NOT EXISTS "auth" (
            token TEXT NOT NULL UNIQUE,
            username TEXT NOT NULL
        );
`

var defaultConfig = []struct {
	key, val string
}{
	{"prompt_update_check", "1"},
	{"update_check", "0"},
	{"startup_minimized", "0"},
	{"startup_autoconnect", "0"},
	{"smart_layers_enabled", "1"},
	{"api_enabled", "0"},
	{"api_port", "50051"},
}
