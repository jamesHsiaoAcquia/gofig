/*
Package gofig simplifies external, runtime configuration of go programs.
*/
package gofig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/akutz/goof"
	"github.com/akutz/gotil"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	yaml "gopkg.in/yaml.v2"
)

var (
	homeDirPath      string
	etcDirPath       string
	usrDirPath       string
	envVarRx         *regexp.Regexp
	registrations    []*Registration
	registrationsRWL *sync.RWMutex
	secureKeys       map[string]*regKey
	secureKeysRWL    *sync.RWMutex
	prefix           string
)

func init() {
	envVarRx = regexp.MustCompile(`^\s*([^#=]+?)=(.+)$`)
	registrationsRWL = &sync.RWMutex{}
	secureKeys = map[string]*regKey{}
	secureKeysRWL = &sync.RWMutex{}
	loadEtcEnvironment()
}

// Config is the interface that enables retrieving configuration information.
type Config interface {

	// SetLogLevel sets the log level for this instance.
	SetLogLevel(level log.Level)

	// GetLogLevel gets the log level for this instance.
	GetLogLevel() log.Level

	// Parent gets the configuration's parent (if set).
	Parent() Config

	// Scope returns a scoped view of the configuration. The specified scope
	// string will be used to prefix all property retrievals via the Get
	// and Set functions. Please note that the other functions will still
	// operate as they would for the non-scoped configuration instance. This
	// includes the AllSettings and AllKeys functions as well; they are *not*
	// scoped.
	Scope(scope string) Config

	// GetScope returns the config's current scope (if any).
	GetScope() string

	// FlagSets gets the config's flag sets.
	FlagSets() map[string]*flag.FlagSet

	// GetString returns the value associated with the key as a string
	GetString(k string) string

	// GetBool returns the value associated with the key as a bool
	GetBool(k string) bool

	// GetStringSlice returns the value associated with the key as a string
	// slice.
	GetStringSlice(k string) []string

	// GetInt returns the value associated with the key as an int
	GetInt(k string) int

	// Get returns the value associated with the key
	Get(k string) interface{}

	// Set sets an override value
	Set(k string, v interface{})

	// IsSet returns a flag indicating whether or not a key is set.
	IsSet(k string) bool

	// Copy creates a copy of this Config instance
	Copy() (Config, error)

	// ToJSON exports this Config instance to a JSON string
	ToJSON() (string, error)

	// ToJSONCompact exports this Config instance to a compact JSON string
	ToJSONCompact() (string, error)

	// MarshalJSON implements the encoding/json.Marshaller interface. It allows
	// this type to provide its own marshalling routine.
	MarshalJSON() ([]byte, error)

	// ReadConfig reads a configuration stream into the current config instance
	ReadConfig(in io.Reader) error

	// ReadConfigFile reads a configuration files into the current config
	// instance
	ReadConfigFile(filePath string) error

	// EnvVars returns an array of the initialized configuration keys as
	// key=value strings where the key is configuration key's environment
	// variable key and the value is the current value for that key.
	EnvVars() []string

	// AllKeys gets a list of all the keys present in this configuration.
	AllKeys() []string

	// AllSettings gets a map of this configuration's settings.
	AllSettings() map[string]interface{}
}

// config contains the configuration information
type config struct {
	*log.Logger
	flagSets map[string]*flag.FlagSet
	v        *viper.Viper
}

// scopedConfig is a scoped configuration information
type scopedConfig struct {
	Config
	scope string
}

// FromJSON initializes a new Config instance from a JSON string
func FromJSON(from string) (Config, error) {
	c := newConfig()
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(from), &m); err != nil {
		return nil, err
	}
	for k, v := range m {
		c.v.Set(k, v)
	}
	return c, nil
}

// SetGlobalConfigPath sets the path of the directory from which the global
// configuration file is read.
func SetGlobalConfigPath(path string) {
	etcDirPath = path
}

// SetUserConfigPath sets the path of the directory from which the user
// configuration file is read.
func SetUserConfigPath(path string) {
	usrDirPath = path
}

// Register registers a new configuration with the config package.
func Register(r *Registration) {
	registrationsRWL.Lock()
	defer registrationsRWL.Unlock()
	registrations = append(registrations, r)
}

// New initializes a new instance of a Config struct
func New() Config {
	return newConfig()
}

// NewConfig initialies a new instance of a Config object with the specified
// options.
func NewConfig(
	loadGlobalConfig, loadUserConfig bool,
	configName, configType string) Config {
	return newConfigWithOptions(
		loadGlobalConfig, loadUserConfig, configName, configType)
}

func (c *config) SetLogLevel(level log.Level) {
	c.Level = level
}

func (c *config) GetLogLevel() log.Level {
	return c.Level
}

func (c *scopedConfig) Parent() Config {
	return c.Config
}
func (c *config) Parent() Config {
	return nil
}

func (c *scopedConfig) Scope(scope string) Config {
	return &scopedConfig{Config: c, scope: scope}
}
func (c *config) Scope(scope string) Config {
	return &scopedConfig{Config: c, scope: scope}
}

func (c *scopedConfig) GetScope() string {
	return c.scope
}
func (c *config) GetScope() string {
	return ""
}

func (c *config) FlagSets() map[string]*flag.FlagSet {
	return c.flagSets
}

func (c *scopedConfig) Copy() (Config, error) {
	cc, err := c.Config.Copy()
	if err != nil {
		return nil, err
	}
	return &scopedConfig{Config: cc, scope: c.scope}, nil
}
func (c *config) Copy() (Config, error) {
	newC := newConfig()
	newC.SetLogLevel(c.GetLogLevel())
	m := map[string]interface{}{}
	c.v.Unmarshal(&m)
	for k, v := range m {
		newC.v.Set(k, v)
	}
	return newC, nil
}

func (c *config) ToJSON() (string, error) {
	buf, err := c.marshalIndentJSON(true)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func (c *config) ToJSONCompact() (string, error) {
	buf, err := c.marshalJSON(true)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func (c *config) MarshalJSON() ([]byte, error) {
	return c.marshalJSON(true)
}

func (c *config) ReadConfig(in io.Reader) error {
	if in == nil {
		return goof.New("config reader is nil")
	}
	return c.v.MergeConfig(in)
}

func (c *config) ReadConfigFile(filePath string) error {
	buf, err := ioutil.ReadFile(filePath)
	if err != nil {
		return err
	}
	return c.ReadConfig(bytes.NewBuffer(buf))
}

func (c *config) EnvVars() []string {
	keyVals := c.allSettings()
	envVars := make(map[string]string)
	c.flattenEnvVars("", keyVals, envVars)
	var evArr []string
	for k, v := range envVars {
		evArr = append(evArr, fmt.Sprintf("%s=%v", k, v))
	}
	return evArr
}

func (c *config) AllKeys() []string {
	ak := []string{}
	as := c.allSettings()

	for k, v := range as {
		switch tv := v.(type) {
		case nil:
			continue
		case map[string]interface{}:
			flattenArrayKeys(k, tv, &ak)
		default:
			ak = append(ak, k)
		}
	}

	return ak
}

func (c *config) AllSettings() map[string]interface{} {
	return c.allSettings()
}

func (c *config) GetString(k string) string {
	c.WithField("key", k).Debug("config.GetString")
	return c.v.GetString(k)
}
func (c *scopedConfig) GetString(k string) string {
	sk := fmt.Sprintf("%s.%s", c.scope, k)
	if c.Config.IsSet(sk) {
		return c.Config.GetString(sk)
	}
	if c.Parent() != nil {
		return c.Parent().GetString(k)
	}
	return ""
}

func (c *config) GetBool(k string) bool {
	c.WithField("key", k).Debug("config.GetBool")
	return c.v.GetBool(k)
}
func (c *scopedConfig) GetBool(k string) bool {
	sk := fmt.Sprintf("%s.%s", c.scope, k)
	if c.Config.IsSet(sk) {
		return c.Config.GetBool(sk)
	}
	if c.Parent() != nil {
		return c.Parent().GetBool(k)
	}
	return false
}

func (c *config) GetStringSlice(k string) []string {
	c.WithField("key", k).Debug("config.GetStringSlice")
	return c.v.GetStringSlice(k)
}
func (c *scopedConfig) GetStringSlice(k string) []string {
	sk := fmt.Sprintf("%s.%s", c.scope, k)
	if c.Config.IsSet(sk) {
		return c.Config.GetStringSlice(sk)
	}
	if c.Parent() != nil {
		return c.Parent().GetStringSlice(k)
	}
	return nil
}

func (c *config) GetInt(k string) int {
	c.WithField("key", k).Debug("config.GetInt")
	return c.v.GetInt(k)
}
func (c *scopedConfig) GetInt(k string) int {
	sk := fmt.Sprintf("%s.%s", c.scope, k)
	if c.Config.IsSet(sk) {
		return c.Config.GetInt(sk)
	}
	if c.Parent() != nil {
		return c.Parent().GetInt(k)
	}
	return 0
}

func (c *config) Get(k string) interface{} {
	c.WithField("key", k).Debug("config.Get")
	return c.v.Get(k)
}
func (c *scopedConfig) Get(k string) interface{} {
	sk := fmt.Sprintf("%s.%s", c.scope, k)
	if c.Config.IsSet(sk) {
		return c.Config.Get(sk)
	}
	if c.Parent() != nil {
		return c.Parent().Get(k)
	}
	return nil
}

func (c *config) IsSet(k string) bool {
	c.WithField("key", k).Debug("config.IsSet")
	return c.v.IsSet(k)
}
func (c *scopedConfig) IsSet(k string) bool {
	if c.Config.IsSet(fmt.Sprintf("%s.%s", c.scope, k)) {
		return true
	}
	if c.Parent() != nil {
		return c.Parent().IsSet(k)
	}
	return false
}

func (c *config) Set(k string, v interface{}) {
	c.v.Set(k, v)
}
func (c *scopedConfig) Set(k string, v interface{}) {
	c.Config.Set(fmt.Sprintf("%s.%s", c.scope, k), v)
}

func newConfig() *config {
	return newConfigWithOptions(true, true, "config", "yml")
}

func newConfigWithOptions(
	loadGlobalConfig, loadUserConfig bool,
	configName, configType string) *config {

	c := &config{
		Logger:   log.New(),
		v:        viper.New(),
		flagSets: map[string]*flag.FlagSet{},
	}
	c.SetLogLevel(log.GetLevel())

	c.Debug("initializing configuration")

	c.v.SetTypeByDefaultValue(false)
	c.v.SetConfigName(configName)
	c.v.SetConfigType(configType)

	c.processRegistrations()

	cfgFile := fmt.Sprintf("%s.%s", configName, configType)
	etcConfigFile := fmt.Sprintf("%s/%s", etcDirPath, cfgFile)
	usrConfigFile := fmt.Sprintf("%s/%s", usrDirPath, cfgFile)

	if loadGlobalConfig && gotil.FileExists(etcConfigFile) {
		c.WithField("path", etcConfigFile).Debug("loading global config file")
		if err := c.ReadConfigFile(etcConfigFile); err != nil {
			c.WithField("path", etcConfigFile).WithError(err).Debug(
				"error reading global config file")
		}
	}

	if loadUserConfig && gotil.FileExists(usrConfigFile) {
		c.WithField("path", usrConfigFile).Debug("loading user config file")
		if err := c.ReadConfigFile(usrConfigFile); err != nil {
			c.WithField("path", usrConfigFile).WithError(err).Debug(
				"error reading user config file")
		}
	}

	return c
}

func (c *config) marshalJSON(secure bool) ([]byte, error) {
	var m map[string]interface{}
	if secure {
		var err error
		if m, err = c.allSecureSettings(); err != nil {
			return nil, err
		}
	} else {
		m = c.allSettings()
	}
	return json.Marshal(m)
}

func (c *config) marshalIndentJSON(secure bool) ([]byte, error) {
	var m map[string]interface{}
	if secure {
		var err error
		if m, err = c.allSecureSettings(); err != nil {
			return nil, err
		}
	} else {
		m = c.allSettings()
	}
	return json.MarshalIndent(m, "", "  ")
}

func (c *config) allSecureSettings() (map[string]interface{}, error) {
	buf, err := json.Marshal(c.allSettings())
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, err
	}

	c.deleteSecureValues("", m)

	return m, err
}

func (c *config) deleteSecureValues(prefix string, m map[string]interface{}) {
	for k, v := range m {
		kk := k
		if prefix != "" {
			kk = fmt.Sprintf("%s.%s", prefix, k)
		}
		if c.isSecureKey(kk) {
			delete(m, k)
		}
		switch tv := v.(type) {
		case map[string]interface{}:
			c.deleteSecureValues(kk, tv)
		}
	}
}

func (c *config) processRegistrations() {
	registrationsRWL.RLock()
	defer registrationsRWL.RUnlock()

	for _, r := range registrations {

		fsn := fmt.Sprintf("%s Flags", r.name)
		fs, ok := c.flagSets[fsn]
		if !ok {
			fs = &flag.FlagSet{}
			c.flagSets[fsn] = fs
		}

		for _, k := range r.keys {

			if fs.Lookup(k.flagName) != nil {
				continue
			}

			evn := k.envVarName

			c.WithFields(log.Fields{
				"keyName":      k.keyName,
				"keyType":      k.keyType,
				"flagName":     k.flagName,
				"envVar":       evn,
				"defaultValue": k.defVal,
				"usage":        k.desc,
			}).Debug("adding flag")

			// bind the environment variable
			c.v.BindEnv(k.keyName, evn)

			if k.short == "" {
				switch k.keyType {
				case String, SecureString:
					fs.String(k.flagName, k.defVal.(string), k.desc)
				case Int:
					fs.Int(k.flagName, k.defVal.(int), k.desc)
				case Bool:
					fs.Bool(k.flagName, k.defVal.(bool), k.desc)
				}
			} else {
				switch k.keyType {
				case String, SecureString:
					fs.StringP(k.flagName, k.short, k.defVal.(string), k.desc)
				case Int:
					fs.IntP(k.flagName, k.short, k.defVal.(int), k.desc)
				case Bool:
					fs.BoolP(k.flagName, k.short, k.defVal.(bool), k.desc)
				}
			}

			c.v.BindPFlag(k.keyName, fs.Lookup(k.flagName))
		}

		// read the config
		if r.yaml != "" {
			c.ReadConfig(bytes.NewReader([]byte(r.yaml)))
		}
	}
}

// flattenEnvVars returns a map of configuration keys coming from a config
// which may have been nested.
func (c *config) flattenEnvVars(
	prefix string, keys map[string]interface{}, envVars map[string]string) {

	for k, v := range keys {

		var kk string
		if prefix == "" {
			kk = k
		} else {
			kk = fmt.Sprintf("%s.%s", prefix, k)
		}
		ek := strings.ToUpper(strings.Replace(kk, ".", "_", -1))

		c.WithFields(log.Fields{
			"key":   kk,
			"value": v,
		}).Debug("flattening env vars")

		switch vt := v.(type) {
		case string:
			envVars[ek] = vt
		case []interface{}:
			var vArr []string
			for _, iv := range vt {
				vArr = append(vArr, iv.(string))
			}
			envVars[ek] = strings.Join(vArr, " ")
		case map[string]interface{}:
			c.flattenEnvVars(kk, vt, envVars)
		case bool:
			envVars[ek] = fmt.Sprintf("%v", vt)
		case int, int32, int64:
			envVars[ek] = fmt.Sprintf("%v", vt)
		}
	}
	return
}

func (c *config) allSettings() map[string]interface{} {
	as := map[string]interface{}{}
	ms := map[string]map[string]interface{}{}

	for k, v := range c.v.AllSettings() {
		switch tv := v.(type) {
		case nil:
			continue
		case map[string]interface{}:
			ms[k] = tv
		default:
			as[k] = tv
		}
	}

	for msk, msv := range ms {
		flat := map[string]interface{}{}
		flattenMapKeys(msk, msv, flat)
		for fk, fv := range flat {
			if asv, ok := as[fk]; ok && reflect.DeepEqual(asv, fv) {
				c.WithFields(log.Fields{
					"key":     fk,
					"valAll":  asv,
					"valFlat": fv,
				}).Debug("deleting duplicate flat val")
				delete(as, fk)
			}
		}
		as[msk] = msv
	}

	return as
}

func flattenArrayKeys(
	prefix string, m map[string]interface{}, flat *[]string) {
	for k, v := range m {
		kk := fmt.Sprintf("%s.%s", prefix, k)
		switch vt := v.(type) {
		case map[string]interface{}:
			flattenArrayKeys(kk, vt, flat)
		default:
			*flat = append(*flat, kk)
		}
	}
}

func flattenMapKeys(
	prefix string, m map[string]interface{}, flat map[string]interface{}) {
	for k, v := range m {
		kk := fmt.Sprintf("%s.%s", prefix, k)
		switch vt := v.(type) {
		case map[string]interface{}:
			flattenMapKeys(kk, vt, flat)
		default:
			flat[strings.ToLower(kk)] = v
		}
	}
}

func loadEtcEnvironment() {
	lr, _ := gotil.LineReaderFrom("/etc/environment")
	if lr == nil {
		return
	}
	for l := range lr {
		m := envVarRx.FindStringSubmatch(l)
		if m == nil || len(m) < 3 || os.Getenv(m[1]) != "" {
			continue
		}
		os.Setenv(m[1], m[2])
	}
}

func (c *config) isSecureKey(k string) bool {
	secureKeysRWL.RLock()
	defer secureKeysRWL.RUnlock()
	kn := strings.ToLower(k)
	_, ok := secureKeys[kn]
	c.WithFields(log.Fields{
		"keyName":  kn,
		"isSecure": ok,
	}).Debug("isSecureKey")
	return ok
}

// ValidateYAML verifies the YAML in the stream is valid.
func ValidateYAML(r io.Reader) (map[interface{}]interface{}, error) {
	b, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	m := map[interface{}]interface{}{}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ValidateYAMLString verifies the YAML string valid.
func ValidateYAMLString(s string) (map[interface{}]interface{}, error) {
	b := &bytes.Buffer{}
	b.WriteString(s)
	return ValidateYAML(b)
}
