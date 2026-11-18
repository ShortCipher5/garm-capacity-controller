package garmapi

// SourceIdentity binds this client's observations to its configured API
// destination using the same identity as pool ownership enrollment. It is not
// proof of provider exclusivity or authentication of a GARM controller ID.
// Bearer-token rotation does not change the destination identity.
func (c *DemandClient) SourceIdentity() string {
	if c == nil || c.discovery == nil || c.discovery.management == nil {
		return ""
	}
	return c.discovery.management.ManagementIdentity()
}
