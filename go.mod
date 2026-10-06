// longfellow-go is the cgo module that carries the native zero-knowledge
// prover, so that irmago does not have to.
//
// The dependency runs one way only: this module imports irmago's stdlib-only
// leaf package eudi/credentials/mdoc/zk and implements the interfaces declared
// there. irmago never imports this module — an application that wants a prover
// imports both and wires them together. That is what keeps irmago buildable
// with CGO_ENABLED=0 and keeps this from becoming a module cycle.
//
// The replace directive points at the working checkout because neither side is
// published yet. It comes out when irmago tags a version carrying the zk
// package.
module github.com/privacybydesign/longfellow-go

go 1.27

require (
	github.com/fxamacker/cbor/v2 v2.9.2
	github.com/privacybydesign/irmago v0.0.0
	github.com/stretchr/testify v1.12.0
	github.com/veraison/go-cose v1.3.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/go-errors/errors v1.5.1 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/jwx-go/es256k/v4 v4.0.4 // indirect
	github.com/lestrrat-go/dsig v1.4.0 // indirect
	github.com/lestrrat-go/dsig-secp256k1 v1.0.0 // indirect
	github.com/lestrrat-go/jwx/v4 v4.4.0 // indirect
	github.com/lestrrat-go/option/v3 v3.0.0-alpha1 // indirect
	github.com/mr-tron/base58 v1.1.3 // indirect
	github.com/privacybydesign/gabi v0.0.0-20221212095008-68a086907750 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/valyala/fastjson v1.6.10 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gorm.io/datatypes v1.2.7 // indirect
	gorm.io/driver/mysql v1.6.0 // indirect
	gorm.io/gorm v1.31.1 // indirect
)

replace github.com/privacybydesign/irmago => ../irmago
