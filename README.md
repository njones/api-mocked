# API-Mocked

API-Mocked is a stand-alone HTTP(s)+ server(s) for serving known (usually static) content.

# Table of Content
- [Overview](#overview)
- [Getting Started](#getting-started)
    * [Simple Config](#using-a-basic-config-file)
- [License](#license)

## Overview

**API-Mocked** is a tool to provide a way to mock an api using a standalone server.

It can mock HTTP, HTTPS and Websockets, so it can provide wide coverage of how an API can look. It has the following features:

1. Uses HCL for a config format, so it's easy to use and copy/paste
2. Allows websocket connections (via socket.io)
3. Can use CORS headers
4. Can use JWTs for auth
5. Can use BasicAuth for auth
6. Can use values from Headers, Query Paths and JWT in responses
7. Can startup multiple servers (i.e http and https) at once
8. Can have HTTP2 only servers 
10. Can have a response on a delay
11. Can have multiple responses 
12. Can have multiple responses on a timer
13. Sends back HPKP header

## Getting Started

**API-Mocked** compiles to a single binary so it can be started with 

```sh
$ api-mocked
```

or 

```sh
$ api-mocked -config basic.hcl
```

### Using a Basic Config File

When you use the `-config` option you can have a file that looks like below:

```hcl
#basic.hcl

version = "0.0.1"

server "service" {
    host = ":8888"
    ssl {
        # lets_encrypt = ["service.api-mocked.com"]
    }
    jwt "test-1" {
        algo = "S256"
        private_key = file("keys/rsa256.key")
    }
}

notfound {
    response "404" {
        body = "Not Found - Check your code."
    }
}

path "/path/to/api" {
     _-= "Return a JWT token as a cookie with a delay of 2s"

    request "get" {
        delay = "2s"
        response "200" {
            jwt "test1-1" "cookie" "access-token" {
               iss = "my issue"
               sub = "my subject"
               nbf = now()
               iat = now()
               exp = duration("1h")
               hello = "world"
           }
        }
    }

    request "post" {
        response "200" {
            body = "Accepted"
        }
    }
}

path "/send/back/ws/{id}" {
    request "get" {
        socketio "connection" {
            broadcast "ns" "event" {
                data = <<_JSON_
{
    "hello": "world"
}
_JSON_
            }
        }
        response "200" {
            body = "Sent"
        }
    }
}


path "/ping" {
    _-= "A simple endpoint to check if things are working"

    request "get" {
        order = "random"
        response "200" {
            body = "OK"
        }

        response "200" {
            body = "Works"
        }

        response "200" {
            body = "Pong"
        }

        # Sometimes return a 500, to see what happens with our application
        response "500" {
            body = "Internal Server Error (OK)"
        }
    }
}
```

## License
MIT License, see [LICENSE](https://github.com/njones/api-mocked/blob/master/LICENSE)
