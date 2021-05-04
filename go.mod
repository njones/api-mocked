module github.com/njones/api-mocked

go 1.16

require (
	github.com/ambelovsky/gosf-socketio v0.0.0-20201109193639-add9d32f8b19
	github.com/brianolson/cbor_go v1.0.0 // indirect
	github.com/caddyserver/certmagic v0.12.0
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/fsnotify/fsnotify v1.4.9
	github.com/go-chi/chi v4.0.2+incompatible
	github.com/google/uuid v1.1.2 // indirect
	github.com/gorilla/websocket v1.4.2 // indirect
	github.com/hashicorp/hcl/v2 v2.7.0
	github.com/jaswdr/faker v1.3.0
	github.com/njones/logger v1.0.8
	github.com/pubnub/go v4.10.0+incompatible
	github.com/rs/xid v1.2.1
	github.com/spf13/afero v1.5.1
	github.com/tidwall/pretty v1.1.0
	github.com/zclconf/go-cty v1.2.0
	plugins/config v0.0.0
	plugins/request v0.0.0
	plugins/response v0.0.0
)

replace (
	plugins/config => ./plugins/config
	plugins/request => ./plugins/request
	plugins/response => ./plugins/response
)
