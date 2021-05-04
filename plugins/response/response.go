package response

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// HTTP is the reponse struct
type HTTP struct {
	Status  string         `hcl:"status,label"`
	Body    *hcl.Attribute `hcl:"body"`
	Headers *struct {
		Data map[string][]cty.Value
	} `hcl:"header,block"`
	JWT *struct {
		Name   string `hcl:"name,label"`
		Output string `hcl:"output,label"`
		Key    string `hcl:"key,label"`

		Subject    *hcl.Attribute    `hcl:"sub" json:"sub"`
		Issuers    *hcl.Attribute    `hcl:"iss" json:"iss"`
		Audience   *hcl.Attribute    `hcl:"aud" json:"aud"`
		Expiration *hcl.Attribute    `hcl:"exp" json:"exp"`
		NotBefore  *hcl.Attribute    `hcl:"nbf" json:"nbf"`
		IssuedAt   *hcl.Attribute    `hcl:"iat" json:"iat"`
		JWTID      *hcl.Attribute    `hcl:"jti" json:"jti"`
		Roles      []string          `hcl:"roles,optional" json:"roles,optional"`
		AuthType   []string          `hcl:"auth_type,optional" json:"auth_type,optional"`
		Payload    map[string]string `hcl:",remain"`
	} `hcl:"jwt,block"`
}
