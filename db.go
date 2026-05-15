// Copyright 2009 The freegeoip authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package freegeoip

import (
	"compress/gzip"
	"crypto/md5"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/howeyc/fsnotify"
	"github.com/oschwald/maxminddb-golang"
)

// ErrUnavailable is returned by DB.Lookup when the database is not yet open.
var ErrUnavailable = errors.New("no database available")

// DB is the IP geolocation database.
type DB struct {
	file        string
	checksum    string
	reader      *maxminddb.Reader
	notifyQuit  chan struct{}
	notifyOpen  chan string
	notifyError chan error
	notifyInfo  chan string
	closed      bool
	lastUpdated time.Time
	mu          sync.RWMutex
}

// Open creates and initializes a DB from a local file.
// The file is monitored by fsnotify and reloaded automatically when changed.
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
		time.Sleep(time.Second)
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
		data, err = ioutil.ReadAll(gzf)
	} else {
		// Not gzipped — reopen and read raw.
		f.Seek(0, 0)
		data, err = ioutil.ReadAll(f)
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

// Date returns the UTC time the database file was last modified.
func (db *DB) Date() time.Time {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.lastUpdated
}

func (db *DB) NotifyClose() <-chan struct{}  { return db.notifyQuit }
func (db *DB) NotifyOpen() <-chan string     { return db.notifyOpen }
func (db *DB) NotifyError() <-chan error     { return db.notifyError }
func (db *DB) NotifyInfo() <-chan string     { return db.notifyInfo }

// Lookup performs a database lookup of the given IP address.
func (db *DB) Lookup(addr net.IP, result interface{}) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.reader == nil {
		return ErrUnavailable
	}
	return db.reader.Lookup(addr, result)
}

// Close closes the database.
func (db *DB) Close() {
	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.closed {
		db.closed = true
		close(db.notifyQuit)
		if db.reader != nil {
			db.reader.Close()
			db.reader = nil
		}
	}
}
