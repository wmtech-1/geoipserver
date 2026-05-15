// Copyright 2009 The freegeoip authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package freegeoip

import (
	"compress/gzip"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/howeyc/fsnotify"
	"github.com/oschwald/maxminddb-golang"
)

// ErrUnavailable may be returned by DB.Lookup when the database
// is not yet available.
var ErrUnavailable = errors.New("no database available")

// DB is the IP geolocation database.
type DB struct {
	file        string            // Database file name.
	checksum    string            // MD5 of the unzipped database file.
	reader      *maxminddb.Reader // Actual db object.
	notifyQuit  chan struct{}      // Stop watch goroutine.
	notifyOpen  chan string        // Notify when a db file is open.
	notifyError chan error         // Notify when an error occurs.
	notifyInfo  chan string        // Notify random actions for logging.
	closed      bool              // Mark this db as closed.
	lastUpdated time.Time         // Last time the db was updated.
	mu          sync.RWMutex      // Protects all the above.
}

// Open creates and initializes a DB from a local file.
//
// The database file is monitored by fsnotify and automatically
// reloads when the file is updated or overwritten.
func Open(dsn string) (*DB, error) {
	db := &DB{
		file:        dsn,
		notifyQuit:  make(chan struct{}),
		notifyOpen:  make(chan string, 1),
		notifyError: make(chan error, 1),
		notifyInfo:  make(chan string, 1),
	}
	err := db.openFile()
	if err != nil {
		db.Close()
		return nil, err
	}
	err = db.watchFile()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("fsnotify failed for %s: %s", dsn, err)
	}
	return db, nil
}

func (db *DB) watchFile() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dbdir := filepath.Dir(db.file)
	if _, err := os.Stat(dbdir); err != nil {
		if err = os.MkdirAll(dbdir, 0755); err != nil {
			return err
		}
	}
	go db.watchEvents(watcher)
	return watcher.Watch(dbdir)
}

func (db *DB) watchEvents(watcher *fsnotify.Watcher) {
	for {
		select {
		case ev := <-watcher.Event:
			if ev.Name == db.file && (ev.IsCreate() || ev.IsModify()) {
				db.openFile()
			}
		case <-watcher.Error:
		case <-db.notifyQuit:
			watcher.Close()
			return
		}
		time.Sleep(time.Second) // Suppress high-rate events.
	}
}

func (db *DB) openFile() error {
	reader, checksum, err := db.newReader(db.file)
	if err != nil {
		return err
	}
	stat, err := os.Stat(db.file)
	if err != nil {
		return err
	}
	db.setReader(reader, stat.ModTime(), checksum)
	return nil
}

func (db *DB) newReader(dbfile string) (*maxminddb.Reader, string, error) {
	f, err := os.Open(dbfile)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	// Support both plain .mmdb and gzip-compressed files.
	var data []byte
	gzf, err := gzip.NewReader(f)
	if err == nil {
		defer gzf.Close()
		data, err = io.ReadAll(gzf)
	} else {
		// Not gzipped — reopen and read raw.
		f.Seek(0, 0)
		data, err = io.ReadAll(f)
	}
	if err != nil {
		return nil, "", err
	}
	checksum := fmt.Sprintf("%x", md5.Sum(data))
	mmdb, err := maxminddb.FromBytes(data)
	return mmdb, checksum, err
}

func (db *DB) setReader(reader *maxminddb.Reader, modtime time.Time, checksum string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		reader.Close()
		return
	}
	if db.reader != nil {
		db.reader.Close()
	}
	db.reader = reader
	db.lastUpdated = modtime.UTC()
	db.checksum = checksum
	select {
	case db.notifyOpen <- db.file:
	default:
	}
}

// Date returns the UTC date the database file was last modified.
// If no database file has been opened the behaviour of Date is undefined.
func (db *DB) Date() time.Time {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.lastUpdated
}

// NotifyClose returns a channel that is closed when the database is closed.
func (db *DB) NotifyClose() <-chan struct{} {
	return db.notifyQuit
}

// NotifyOpen returns a channel that notifies when a new database is
// loaded or reloaded.
func (db *DB) NotifyOpen() (filename <-chan string) {
	return db.notifyOpen
}

// NotifyError returns a channel that notifies when an error occurs
// while reloading a DB.
func (db *DB) NotifyError() (errChan <-chan error) {
	return db.notifyError
}

// NotifyInfo returns a channel that notifies informational messages.
func (db *DB) NotifyInfo() <-chan string {
	return db.notifyInfo
}

// Lookup performs a database lookup of the given IP address, and stores
// the response into the result value. The result value must be a struct
// with specific fields and tags as described here:
// https://godoc.org/github.com/oschwald/maxminddb-golang#Reader.Lookup
//
// See the DefaultQuery for an example of the result struct.
func (db *DB) Lookup(addr net.IP, result interface{}) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.reader != nil {
		return db.reader.Lookup(addr, result)
	}
	return ErrUnavailable
}

// DefaultQuery is the default query used for database lookups.
type DefaultQuery struct {
	Continent struct {
		ISOCode string            `maxminddb:"code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Region []struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
		MetroCode uint    `maxminddb:"metro_code"`
		TimeZone  string  `maxminddb:"time_zone"`
	} `maxminddb:"location"`
	Postal struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"postal"`
}

// Close closes the database.
func (db *DB) Close() {
	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.closed {
		db.closed = true
		close(db.notifyQuit)
		close(db.notifyOpen)
		close(db.notifyError)
		close(db.notifyInfo)
	}
	if db.reader != nil {
		db.reader.Close()
		db.reader = nil
	}
}
