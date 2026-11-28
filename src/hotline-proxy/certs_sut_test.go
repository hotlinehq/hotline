package proxy_test

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
