package etcdstore

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	ginsessions "github.com/gin-contrib/sessions"
	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Amount of time for cookies/etcd keys to expire.
var sessionExpire = 86400 * 30

// SessionSerializer provides an interface hook for alternative serializers
type SessionSerializer interface {
	Deserialize(d []byte, ss *sessions.Session) error
	Serialize(ss *sessions.Session) ([]byte, error)
}

// JSONSerializer encode the session map to JSON.
type JSONSerializer struct{}

// Serialize to JSON. Will err if there are unmarshalable key values
func (s JSONSerializer) Serialize(ss *sessions.Session) ([]byte, error) {
	m := make(map[string]interface{}, len(ss.Values))
	for k, v := range ss.Values {
		ks, ok := k.(string)
		if !ok {
			err := fmt.Errorf("Non-string key value, cannot serialize session to JSON: %v", k)
			fmt.Printf("etcdstore.JSONSerializer.serialize() Error: %v", err)
			return nil, err
		}
		m[ks] = v
	}
	return json.Marshal(m)
}

// Deserialize back to map[string]interface{}
func (s JSONSerializer) Deserialize(d []byte, ss *sessions.Session) error {
	m := make(map[string]interface{})
	err := json.Unmarshal(d, &m)
	if err != nil {
		fmt.Printf("etcdstore.JSONSerializer.deserialize() Error: %v", err)
		return err
	}
	for k, v := range m {
		ss.Values[k] = v
	}
	return nil
}

// GobSerializer uses gob package to encode the session map
type GobSerializer struct{}

// Serialize using gob
func (s GobSerializer) Serialize(ss *sessions.Session) ([]byte, error) {
	buf := new(bytes.Buffer)
	enc := gob.NewEncoder(buf)
	err := enc.Encode(ss.Values)
	if err == nil {
		return buf.Bytes(), nil
	}
	return nil, err
}

// Deserialize back to map[interface{}]interface{}
func (s GobSerializer) Deserialize(d []byte, ss *sessions.Session) error {
	dec := gob.NewDecoder(bytes.NewBuffer(d))
	return dec.Decode(&ss.Values)
}

// EtcdStore stores sessions in a etcd backend.
type EtcdStore struct {
	client        *clientv3.Client
	Codecs        []securecookie.Codec
	options       *sessions.Options // default configuration
	DefaultMaxAge int               // default Etcd TTL for a MaxAge == 0 session
	maxLength     int
	keyPrefix     string
	serializer    SessionSerializer
}

// SetMaxLength sets EtcdStore.maxLength if the `l` argument is greater or equal 0
// maxLength restricts the maximum length of new sessions to l.
// If l is 0 there is no limit to the size of a session, use with caution.
// The default for a new EtcdStore is 4096. Etcd allows for max.
// value sizes of up to 1.5MB (https://etcd.io/docs/v3.4/dev-guide/limit/)
// Default: 4096,
func (s *EtcdStore) SetMaxLength(l int) {
	if l >= 0 {
		s.maxLength = l
	}
}

// SetKeyPrefix set the prefix
func (s *EtcdStore) SetKeyPrefix(p string) {
	s.keyPrefix = p
}

// SetSerializer sets the serializer
func (s *EtcdStore) SetSerializer(ss SessionSerializer) {
	s.serializer = ss
}

// SetMaxAge restricts the maximum age, in seconds, of the session record
// both in database and a browser. This is to change session storage configuration.
// If you want just to remove session use your session `s` object and change it's
// `Options.MaxAge` to -1, as specified in
//    http://godoc.org/github.com/gorilla/sessions#Options
//
// Default is the one provided by this package value - `sessionExpire`.
// Set it to 0 for no restriction.
// Because we use `MaxAge` also in SecureCookie crypting algorithm you should
// use this function to change `MaxAge` value.
func (s *EtcdStore) SetMaxAge(v int) {
	var c *securecookie.SecureCookie
	var ok bool
	s.options.MaxAge = v
	for i := range s.Codecs {
		if c, ok = s.Codecs[i].(*securecookie.SecureCookie); ok {
			c.MaxAge(v)
		} else {
			fmt.Printf("Can't change MaxAge on codec %v\n", s.Codecs[i])
		}
	}
}

// NewEtcdStore instantiates a EtcdStore with a *clientv3.Config passed in.
func NewEtcdStore(client *clientv3.Client, keyPairs ...[]byte) *EtcdStore {
	es := &EtcdStore{
		client: client,
		Codecs: securecookie.CodecsFromPairs(keyPairs...),
		options: &sessions.Options{
			Path:   "/",
			MaxAge: sessionExpire,
		},
		DefaultMaxAge: 60 * 20, // 20 minutes seems like a reasonable default
		maxLength:     4096,
		keyPrefix:     "session_",
		serializer:    GobSerializer{},
	}

	return es
}

// NewEtcdStore instantiates a EtcdStore with a *clientv3.Config passed in.
func NewEtcdGinStore(client *clientv3.Client, keyPairs ...[]byte) ginsessions.Store {
	es := &EtcdStore{
		client: client,
		Codecs: securecookie.CodecsFromPairs(keyPairs...),
		options: &sessions.Options{
			Path:   "/",
			MaxAge: sessionExpire,
		},
		DefaultMaxAge: 60 * 20, // 20 minutes seems like a reasonable default
		maxLength:     4096,
		keyPrefix:     "session_",
		serializer:    GobSerializer{},
	}

	return es
}

// Close closes the underlying *etcd.Pool
func (s *EtcdStore) Close() error {
	return s.client.Close()
}

// Get returns a session for the given name after adding it to the registry.
//
// See gorilla/sessions FilesystemStore.Get().
func (s *EtcdStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(s, name)
}

// New returns a session for the given name without adding it to the registry.
//
// See gorilla/sessions FilesystemStore.New().
func (s *EtcdStore) New(r *http.Request, name string) (*sessions.Session, error) {
	var (
		err error
		ok  bool
	)
	session := sessions.NewSession(s, name)
	// make a copy
	options := *s.options
	session.Options = &options
	session.IsNew = true
	if c, errCookie := r.Cookie(name); errCookie == nil {
		err = securecookie.DecodeMulti(name, c.Value, &session.ID, s.Codecs...)
		if err == nil {
			ok, err = s.load(r.Context(), session)
			session.IsNew = !(err == nil && ok) // not new if no error and data available
		}
	}
	return session, err
}

// Save adds a single session to the response.
func (s *EtcdStore) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	// Marked for deletion.
	if session.Options.MaxAge <= 0 {
		if err := s.delete(r.Context(), session); err != nil {
			return err
		}
		http.SetCookie(w, sessions.NewCookie(session.Name(), "", session.Options))
	} else {
		// Build an alphanumeric key for the etcd store.
		if session.ID == "" {
			session.ID = strings.TrimRight(base32.StdEncoding.EncodeToString(securecookie.GenerateRandomKey(32)), "=")
		}
		if err := s.save(r.Context(), session); err != nil {
			return err
		}
		encoded, err := securecookie.EncodeMulti(session.Name(), session.ID, s.Codecs...)
		if err != nil {
			return err
		}
		http.SetCookie(w, sessions.NewCookie(session.Name(), encoded, session.Options))
	}
	return nil
}

// Delete removes the session from etcd, and sets the cookie to expire.
//
// WARNING: This method should be considered deprecated since it is not exposed via the gorilla/sessions interface.
// Set session.Options.MaxAge = -1 and call Save instead. - July 18th, 2013
func (s *EtcdStore) Delete(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	if _, err := s.client.Delete(r.Context(), s.keyPrefix+session.ID); err != nil {
		return err
	}
	// Set cookie to expire.
	options := *session.Options
	options.MaxAge = -1
	http.SetCookie(w, sessions.NewCookie(session.Name(), "", &options))
	// Clear session values.
	for k := range session.Values {
		delete(session.Values, k)
	}
	return nil
}

// Options sets configuration for a session.
//
// See gin-contrib/sessions https://github.com/gin-contrib/sessions/blob/master/sessions.go
func (s *EtcdStore) Options(opts ginsessions.Options) {
	s.options = opts.ToGorillaOptions()
}

// save stores the session in etcd.
func (s *EtcdStore) save(ctx context.Context, session *sessions.Session) error {
	b, err := s.serializer.Serialize(session)
	if err != nil {
		return err
	}
	if s.maxLength != 0 && len(b) > s.maxLength {
		return errors.New("SessionStore: the value to store is too big")
	}

	age := session.Options.MaxAge
	if age == 0 {
		age = s.DefaultMaxAge
	}

	lease, err := s.client.Grant(ctx, int64(age))
	if err != nil {
		return err
	}

	_, err = s.client.Put(ctx, s.keyPrefix+session.ID, string(b), clientv3.WithLease(lease.ID))
	return err
}

// load reads the session from etcd.
// returns true if there is a sessoin data in DB
func (s *EtcdStore) load(ctx context.Context, session *sessions.Session) (bool, error) {

	data, err := s.client.Get(ctx, s.keyPrefix+session.ID)
	if err != nil {
		return false, err
	}

	if data == nil || len(data.Kvs) != 1 {
		return false, nil // no data was associated with this key
	}

	return true, s.serializer.Deserialize(data.Kvs[0].Value, session)
}

// delete removes keys from etcd if MaxAge<0
func (s *EtcdStore) delete(ctx context.Context, session *sessions.Session) error {
	if _, err := s.client.Delete(ctx, s.keyPrefix+session.ID); err != nil {
		return err
	}
	return nil
}
