package session

// RememberIdentity retains a successful verification's identity for the
// current session, avoiding a second identity request for command metadata.
// It never replaces live authentication checks or stores identity on disk.
func RememberIdentity(s Session, id Identity) {
	if r, ok := s.(interface{ RememberIdentity(Identity) }); ok {
		r.RememberIdentity(id)
	}
}

func (o observer) RememberIdentity(id Identity) { RememberIdentity(o.Session, id) }
