package proxy_test

import "strings"

// certsSUT is the config's certs section: the certificate authorities the
// proxy signs with or checks against, as the scenario named them.
type certsSUT struct {
	proxy *proxySUT
	// certEntries is the config's certs section, when a step wrote one.
	certEntries string
	// interception is the certificate authority in the files the connect
	// part names, and second the one that replaced it.
	interception *authority
	second       *authority
}

func newCertsSUT(root *proxySUT) *certsSUT {
	sut := &certsSUT{proxy: root}
	root.addConfig(configCerts, func() string { return sut.certEntries })
	return sut
}

// replaceEntries makes entries the whole certs section.
func (sut *certsSUT) replaceEntries(entries ...string) {
	sut.certEntries = "certs:\n" + strings.Join(entries, "")
}

// add adds an entry to the certs section.
func (sut *certsSUT) add(entry string) { sut.certEntries += entry }

// addOnce adds the entry for name, unless the certs section has one.
func (sut *certsSUT) addOnce(name, entry string) {
	if sut.certEntries == "" {
		sut.certEntries = "certs:\n"
	}
	if !strings.Contains(sut.certEntries, "  "+name+":") {
		sut.certEntries += entry
	}
}

// rememberReplacement remembers the certificate authority that replaced the
// interception one in its files.
func (sut *certsSUT) rememberReplacement(second *authority) { sut.second = second }

// interceptionAuthority is the certificate authority in the files the
// connect part names, and replacement the one that replaced it, if any.
func (sut *certsSUT) interceptionAuthority() *authority { return sut.interception }
func (sut *certsSUT) replacement() *authority           { return sut.second }
