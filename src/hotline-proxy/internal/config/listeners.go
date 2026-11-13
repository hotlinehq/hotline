package config

import (
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"maps"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Listener is one port or socket file the proxy opens, with what it does
// with the connections it takes (Peter, 2026-11-13). Without transparent or
// capture it is a forward proxy port: apps send it their calls, over TLS
// with tls set, and open tunnels with connect set.
type Listener struct {
	// Name is the listener's name in the config.
	Name string
	// Network is tcp for a port, unix for a socket file; Address is the
	// host and port, or the socket file's path with ~/ already read as the
	// home folder.
	Network string
	Address string
	// TCP says the listener relays bytes untouched and reads nothing:
	// protocol tcp. Only transparent and capture listeners may.
	TCP bool
	// TLS, when set, makes the listener take only TLS.
	TLS *ListenerTLS
	// Connect, when set, makes CONNECT an ingress on the listener.
	Connect *Connect
	// Transparent, when set, makes the listener take connections a
	// firewall sent to it, which carry no CONNECT.
	Transparent *Intercept
	// Capture, when set, makes the listener's socket file the ingress of
	// the macOS capture extension.
	Capture *Capture
}

// URL is the listener's address as the config writes it.
func (l *Listener) URL() string { return l.Network + "://" + l.Address }

// Shape is what a reload cannot change without a restart: the address,
// the protocol and which parts the listener has.
func (l *Listener) Shape() string {
	return fmt.Sprintf("%s tcp=%t tls=%t sign=%t connect=%t transparent=%t capture=%t",
		l.URL(), l.TCP, l.TLS != nil, l.TLS != nil && l.TLS.Sign, l.Connect != nil, l.Transparent != nil, l.Capture != nil)
}

// ListenerTLS is the certificate a listener, or the admin port, shows apps:
// the pair as it is, or with Sign set, a certificate signed with it for
// whatever name the app calls.
type ListenerTLS struct {
	Cert *TLS
	Sign bool
}

// Connect is the CONNECT part of a listener: the issuer that signs the
// certificate shown for each vendor host inside a tunnel, and the sites
// relayed untouched.
type Connect struct {
	Issuer      *TLS
	Passthrough Passthrough
}

// Admin is the admin port: health checks, and the profile pages when on.
type Admin struct {
	Network string
	Address string
	TLS     *ListenerTLS
}

// URL is the admin port's address as the config writes it.
func (a *Admin) URL() string { return a.Network + "://" + a.Address }

// Listener finds a listener by name, nil when there is none.
func (c *Config) Listener(name string) *Listener {
	for _, l := range c.Listeners {
		if l.Name == name {
			return l
		}
	}
	return nil
}

// adminFile is the admin section.
type adminFile struct {
	Address string    `yaml:"address"`
	TLS     yaml.Node `yaml:"tls"`
}

// listenerFile is one entry of the listeners section. Its parts are nodes,
// so that a part written with nothing after it still switches it on.
type listenerFile struct {
	Address     string    `yaml:"address"`
	Protocol    string    `yaml:"protocol"`
	Cert        string    `yaml:"cert"`
	TLS         yaml.Node `yaml:"tls"`
	Connect     yaml.Node `yaml:"connect"`
	Transparent yaml.Node `yaml:"transparent"`
	Capture     yaml.Node `yaml:"capture"`
}

// tlsFile is the tls part of a listener or of the admin port.
type tlsFile struct {
	Cert string `yaml:"cert"`
	Mode string `yaml:"mode"`
}

// connectFile is the connect part of a listener.
type connectFile struct {
	Cert        string   `yaml:"cert"`
	Mode        string   `yaml:"mode"`
	Passthrough []string `yaml:"passthrough"`
}

// interceptFile is the transparent part of a listener.
type interceptFile struct {
	Cert        string   `yaml:"cert"`
	Mode        string   `yaml:"mode"`
	Integration string   `yaml:"integration"`
	Passthrough []string `yaml:"passthrough"`
	ECH         struct {
		Relay []string `yaml:"relay"`
	} `yaml:"ech"`
}

// captureFile is the capture part of a listener.
type captureFile struct {
	Cert        string   `yaml:"cert"`
	Mode        string   `yaml:"mode"`
	Passthrough []string `yaml:"passthrough"`
	// Ports are the ports read as calls; 80 and 443 when left out.
	Ports *[]int `yaml:"ports"`
}

// part reads a part of a listener into f. It says whether the part is
// written at all: with nothing after it, the part is on with its defaults.
func (l *loader) part(label string, node yaml.Node, f any) bool {
	switch {
	case node.Kind == 0:
		return false
	case node.Kind == yaml.ScalarNode && node.Tag == "!!null":
		return true
	case node.Kind != yaml.MappingNode:
		l.problem("%s: write its settings as key: value, or nothing for the defaults", label)
		return true
	}
	if err := node.Decode(f); err != nil {
		l.problem("%s: %v", label, err)
	}
	known := map[string]bool{}
	t := reflect.TypeOf(f).Elem()
	for i := range t.NumField() {
		known[t.Field(i).Tag.Get("yaml")] = true
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key := node.Content[i]; !known[key.Value] {
			l.warnings = append(l.warnings, fmt.Sprintf("line %d: field %s is not a setting and is ignored", key.Line, key.Value))
		}
	}
	return true
}

// certs reads the certs section: named cert and key pairs, each checked
// to match and not to have expired. Whether a pair may sign is checked
// where it is used.
func (l *loader) certs(written map[string]*pairFile, now time.Time) map[string]*TLS {
	certs := map[string]*TLS{}
	for _, name := range slices.Sorted(maps.Keys(written)) {
		if written[name] == nil {
			l.problem("cert %s needs both cert and key", name)
			continue
		}
		label := "cert " + name
		certs[name] = l.pair(label, written[name], func(cert, key string, now time.Time) (*tls.Certificate, error) {
			return loadPair(label, cert, key, now)
		}, now)
	}
	return certs
}

// use finds the cert a part names, or the listener's, and checks that it
// fits the mode: one that signs must be a certificate authority, and one
// shown as it is must not be.
func (l *loader) use(label, own, listener string, sign bool, certs map[string]*TLS) *TLS {
	name := cmp.Or(own, listener)
	if name == "" {
		l.problem("%s needs a cert, as cert: name, on it or on the listener", label)
		return nil
	}
	t, known := certs[name]
	switch {
	case !known:
		l.problem("%s names the cert %s, which is not in certs", label, name)
		return nil
	case t == nil || t.Certificate == nil:
		// The pair itself failed to load, which is already a problem.
		return t
	}
	leaf := t.Certificate.Leaf
	authority := leaf.IsCA && (leaf.KeyUsage == 0 || leaf.KeyUsage&x509.KeyUsageCertSign != 0)
	switch {
	case sign && !authority:
		l.problem("%s: cert %s is not a certificate authority, so it cannot sign; use it on tls, where it is shown as it is", label, name)
	case !sign && authority:
		l.problem("%s: cert %s is a certificate authority; write mode: sign to sign with it", label, name)
	}
	return t
}

// listenerTLS reads a tls part: shown as it is unless mode is sign.
func (l *loader) listenerTLS(label string, f tlsFile, listenerCert string, certs map[string]*TLS) *ListenerTLS {
	t := &ListenerTLS{}
	switch f.Mode {
	case "", "serve":
	case "sign":
		t.Sign = true
	default:
		l.problem("%s: mode must be serve or sign", label)
	}
	t.Cert = l.use(label, f.Cert, listenerCert, t.Sign, certs)
	return t
}

// signs checks the mode of a part that can only sign.
func (l *loader) signs(label, mode string) {
	switch mode {
	case "", "sign":
	case "serve":
		l.problem("%s always signs a certificate for each site; mode serve is only for tls", label)
	default:
		l.problem("%s: mode must be sign", label)
	}
}

// address reads tcp://host:port or unix://path.
func (l *loader) address(label, address string) (network, rest string) {
	switch {
	case address == "":
		l.problem("%s needs an address, as tcp://host:port or unix://path", label)
	case strings.HasPrefix(address, "tcp://"):
		rest = strings.TrimPrefix(address, "tcp://")
		if _, port, err := net.SplitHostPort(rest); err != nil || port == "" {
			l.problem("%s: address %s must be tcp://host:port", label, address)
		}
		return "tcp", rest
	case strings.HasPrefix(address, "unix://"):
		return "unix", l.socketPath(label, strings.TrimPrefix(address, "unix://"))
	default:
		l.problem("%s: address %s must start with tcp:// or unix://", label, address)
	}
	return "", ""
}

// socketPath checks a socket file's path: absolute, or in the home folder,
// and short enough for a socket file.
func (l *loader) socketPath(label, path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		// Without HOME, or with a relative one, the path is relative and
		// refused below.
		home, _ := l.lookupEnv("HOME")
		path = home + "/" + rest
	}
	if !strings.HasPrefix(path, "/") {
		l.problem("%s: a socket file must be an absolute path or start with ~/", label)
		return ""
	}
	if len(path) > maxSocketPath {
		l.problem("%s: the path is longer than %d bytes", label, maxSocketPath)
		return ""
	}
	return path
}

// admin reads the admin section.
func (l *loader) admin(f *adminFile, certs map[string]*TLS) *Admin {
	if f == nil {
		l.warnings = append(l.warnings, "there is no admin address, so the proxy has no health checks")
		return nil
	}
	a := &Admin{}
	a.Network, a.Address = l.address("admin", f.Address)
	var t tlsFile
	if l.part("admin: tls", f.TLS, &t) {
		a.TLS = l.listenerTLS("admin: tls", t, "", certs)
	}
	return a
}

// listeners reads the listeners section, sorted by name.
func (l *loader) listeners(written map[string]*listenerFile, certs map[string]*TLS) []*Listener {
	var all []*Listener
	for _, name := range slices.Sorted(maps.Keys(written)) {
		label := "listener " + name
		f := written[name]
		if f == nil {
			l.problem("%s needs an address, as tcp://host:port or unix://path", label)
			continue
		}
		all = append(all, l.listener(label, name, f, certs))
	}
	return all
}

func (l *loader) listener(label, name string, f *listenerFile, certs map[string]*TLS) *Listener {
	li := &Listener{Name: name}
	li.Network, li.Address = l.address(label, f.Address)
	switch f.Protocol {
	case "", "http":
	case "tcp":
		li.TCP = true
	default:
		l.problem("%s: protocol must be http or tcp", label)
	}
	var tlsF tlsFile
	var connectF connectFile
	var transparentF interceptFile
	var captureF captureFile
	hasTLS := l.part(label+": tls", f.TLS, &tlsF)
	hasConnect := l.part(label+": connect", f.Connect, &connectF)
	hasTransparent := l.part(label+": transparent", f.Transparent, &transparentF)
	hasCapture := l.part(label+": capture", f.Capture, &captureF)
	switch {
	case (hasTransparent || hasCapture) && btoi(hasTLS)+btoi(hasConnect)+btoi(hasTransparent)+btoi(hasCapture) > 1:
		l.problem("%s: transparent and capture each need a listener of their own, without tls, connect or the other", label)
	case li.TCP && !hasTransparent && !hasCapture:
		l.problem("%s: protocol tcp is only for transparent and capture listeners", label)
	case hasTransparent && li.Network == "unix":
		l.problem("%s: transparent needs a tcp:// address, where the firewall sends connections", label)
	case hasCapture && li.Network == "tcp":
		l.problem("%s: capture needs a unix:// socket file, which only the proxy's user and root can open", label)
	}
	if li.TCP {
		if f.Cert != "" || transparentF.Cert != "" || captureF.Cert != "" || captureF.Ports != nil ||
			len(transparentF.Passthrough) > 0 || len(captureF.Passthrough) > 0 || len(transparentF.ECH.Relay) > 0 {
			l.problem("%s: with protocol tcp nothing is read, so cert, ports, passthrough and ech are not settings", label)
		}
		if hasTransparent {
			li.Transparent = &Intercept{Integration: transparentF.Integration}
		}
		if hasCapture {
			li.Capture = &Capture{ports: map[string]bool{}}
		}
		return li
	}
	if f.Cert != "" && !hasTLS && !hasConnect && !hasTransparent && !hasCapture {
		l.warnings = append(l.warnings, label+": cert is not used, since the listener has no tls, connect, transparent or capture")
	}
	if hasTLS {
		li.TLS = l.listenerTLS(label+": tls", tlsF, f.Cert, certs)
	}
	if hasConnect {
		l.signs(label+": connect", connectF.Mode)
		li.Connect = &Connect{
			Issuer:      l.use(label+": connect", connectF.Cert, f.Cert, true, certs),
			Passthrough: l.passthrough(label+": connect", connectF.Passthrough),
		}
	}
	if hasTransparent {
		li.Transparent = l.transparent(label+": transparent", transparentF, f.Cert, certs)
	}
	if hasCapture {
		li.Capture = l.capture(label+": capture", captureF, f.Cert, certs)
	}
	return li
}

func (l *loader) transparent(label string, f interceptFile, listenerCert string, certs map[string]*TLS) *Intercept {
	l.signs(label, f.Mode)
	i := &Intercept{
		Integration: f.Integration,
		Issuer:      l.use(label, f.Cert, listenerCert, true, certs),
		Passthrough: l.passthrough(label, f.Passthrough),
	}
	for _, name := range f.ECH.Relay {
		if validPattern(name, false) {
			i.ECHRelay = append(i.ECHRelay, strings.ToLower(name))
		} else {
			l.problem("%s: ech.relay %q must be a host name", label, name)
		}
	}
	return i
}

// capture reads a capture part: the ports read as calls, 80 and 443 when
// left out, the issuer, and the sites relayed untouched.
func (l *loader) capture(label string, f captureFile, listenerCert string, certs map[string]*TLS) *Capture {
	l.signs(label, f.Mode)
	ports := []int{80, 443}
	if f.Ports != nil {
		ports = *f.Ports
	}
	read := map[string]bool{}
	for _, port := range ports {
		if port < 1 || port > 65535 {
			l.problem("%s: port %d must be from 1 to 65535", label, port)
		}
		read[strconv.Itoa(port)] = true
	}
	return &Capture{
		Issuer:      l.use(label, f.Cert, listenerCert, true, certs),
		Passthrough: l.passthrough(label, f.Passthrough),
		ports:       read,
	}
}

// sameAddresses refuses two listeners, or a listener and the admin port,
// on one address.
func (l *loader) sameAddresses(admin *Admin, listeners []*Listener) {
	seen := map[string]string{}
	if admin != nil && admin.Address != "" {
		seen[admin.URL()] = "admin"
	}
	for _, li := range listeners {
		if li.Address == "" {
			continue
		}
		if other, taken := seen[li.URL()]; taken {
			l.problem("%s and listener %s both use the address %s", other, li.Name, li.URL())
			continue
		}
		seen[li.URL()] = "listener " + li.Name
	}
}
