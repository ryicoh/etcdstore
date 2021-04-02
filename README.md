# etcdstore

[![Go](https://github.com/ryicoh/etcdstore/actions/workflows/go.yml/badge.svg)](https://github.com/ryicoh/etcdstore/actions/workflows/go.yml)


A session store backend for [gorilla/sessions](http://www.gorillatoolkit.org/pkg/sessions) - [src](https://github.com/gorilla/sessions).

## Installation

    go get github.com/ryicoh/etcdstore

## Documentation

See http://www.gorillatoolkit.org/pkg/sessions for full documentation on underlying interface.

### Example
``` go
package main

import (
	"github.com/ryicoh/etcdstore"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func main() {
	client, err := clientv3.New(clientv3.Config{Endpoint: []string{"http://localhost:2379"}})
	if err != nil {
		panic(err)
	}
	defer client.Close()

	// Fetch new store.
	store, err := etcdstore.NewEtcdStore(client, []byte("secret-key"))
	if err != nil {
		panic(err)
	}

	// Get a session.
	session, err = store.Get(req, "session-key")
	if err != nil {
		log.Error(err.Error())
	}

	// Add a value.
	session.Values["foo"] = "bar"

	// Save.
	if err = sessions.Save(req, rsp); err != nil {
		t.Fatalf("Error saving session: %v", err)
	}

	// Delete session.
	session.Options.MaxAge = -1
	if err = sessions.Save(req, rsp); err != nil {
		t.Fatalf("Error saving session: %v", err)
	}

	// Change session storage configuration for MaxAge = 10 days.
	store.SetMaxAge(10 * 24 * 3600)
}
```
