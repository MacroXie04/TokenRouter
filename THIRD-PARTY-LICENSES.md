# Third-Party Licenses

This file summarizes direct third-party dependencies used by distributed builds
of TokenRouter. Keep it with Docker images, standalone binaries, and frontend
bundles.

## Backend (Go)

| Dependency | License |
|---|---|
| github.com/gin-gonic/gin | MIT |
| github.com/gin-contrib/cors | MIT |
| github.com/gin-contrib/gzip | MIT |
| gorm.io/gorm | MIT |
| gorm.io/driver/mysql | MIT |
| gorm.io/driver/postgres | MIT |
| github.com/glebarez/sqlite | MIT |
| github.com/go-redis/redis/v8 | BSD-2-Clause |
| github.com/golang-jwt/jwt/v5 | MIT |
| github.com/google/uuid | BSD-3-Clause |
| github.com/gorilla/websocket | BSD-2-Clause |
| github.com/joho/godotenv | MIT |
| github.com/nicksnyder/go-i18n/v2 | MIT |
| github.com/pquerna/otp | Apache-2.0 |
| github.com/samber/lo | MIT |
| github.com/shopspring/decimal | MIT |
| github.com/stretchr/testify | MIT |
| github.com/tidwall/gjson | MIT |
| github.com/tidwall/sjson | MIT |
| github.com/tiktoken-go/tokenizer | MIT |
| github.com/json-iterator/go | MIT |
| github.com/casbin/casbin/v2 | Apache-2.0 |
| github.com/expr-lang/expr | MIT |
| github.com/alicebob/miniredis/v2 | MIT |
| golang.org/x/crypto | BSD-3-Clause |
| golang.org/x/net | BSD-3-Clause |
| gopkg.in/yaml.v3 | MIT |

Transitive dependencies should be audited before a final external release.
