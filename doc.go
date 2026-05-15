// Copyright 2009 The freegeoip authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package freegeoip provides an API for searching the geolocation of IP
// addresses. It uses local database files for both city and ASN lookups.
//
// Local databases are monitored by fsnotify and reloaded automatically
// when the file is updated or overwritten.
package freegeoip
