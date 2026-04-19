# Coding Standards

## Priority Order

Follow coding standards in this order:
1. Existing patterns in the codebase
2. Conventions documented in this file
3. Effective Go (https://go.dev/doc/effective_go) for Go code

---

## Go

### Formatting
- Run `gofmt` / `goimports` — all Go code must be formatted
- Tabs for indentation (enforced by gofmt)
- Line length: no hard limit; prefer readability over brevity

### Naming Conventions
- **Packages**: lowercase, single word (e.g., `checker`, `wpcom`, `audit`)
- **Exported identifiers**: `PascalCase`
- **Unexported identifiers**: `camelCase`
- **Acronyms**: all-caps when exported (`HTTPCode`, `RTTMs`, `URL`), lowercase otherwise (`httpCode`)
- **Error variables**: `ErrFoo` for sentinel errors
- **Interfaces**: noun or `-er` suffix (`Checker`, `Client`)
- **Constants**: `PascalCase` for Go constants; config key strings use `SCREAMING_SNAKE_CASE` to match existing JSON keys

### Error Handling
- Return errors; do not panic in library code
- Wrap with context: `fmt.Errorf("connect: %w", err)`
- Log and continue for non-fatal errors; `log.Fatalf` only at startup

### Concurrency
- Pass `context.Context` as the first argument to any function that blocks or does I/O
- Use `sync/atomic` for hot-path counters; `sync.Mutex` for struct guards
- Never share mutable state across goroutines without synchronisation
- Prefer buffered channels sized to the expected burst; document the rationale

### Imports
- Standard library first, then external, then internal — separated by blank lines
- Alias internal `grpc` package as `vgrpc` to avoid collision with `google.golang.org/grpc`
- Alias `"context"` as `stdctx` only when the local scope shadows the package name

### Comments
- Package comment on every package (`// Package foo ...`)
- Exported symbol comments are required (`// Foo does ...`)
- Inline comments explain *why*, not *what*

---

## Legacy (JavaScript / C++)

The codebase was previously Node.js + C++. Those conventions are no longer relevant — the Go section above takes full precedence. The sections below are retained only as historical reference and should not be followed for new work.

---

## JavaScript

### Formatting
- **Indentation**: Tabs (not spaces)
- **Quotes**: Single quotes for strings
- **Semicolons**: Required
- **Spaces inside parentheses**: `function( arg )`, `require( 'module' )`, `if ( condition )`
- **Spaces inside brackets**: `arr[ index ]`, `obj[ key ]`
- **Braces**: Same line as control structure

```javascript
// Correct
function processData( data ) {
    if ( undefined !== data ) {
        return arr[ index ];
    }
}
require( './config' );

// Incorrect
function processData(data) {
    if (undefined !== data) {
        return arr[index];
    }
}
require('./config');
```

### Naming Conventions
- **Constants**: `SCREAMING_SNAKE_CASE` at file top
- **Global variables**: Prefix with `g` (e.g., `gCountSuccess`, `gCountError`)
- **Local variables**: `camelCase`
- **Functions**: `camelCase` for regular functions, `PascalCase` for constructor-like
- **Module objects**: `camelCase` (e.g., `statsdClient`, `database`)

```javascript
// Correct
const SITE_DOWN = 0;
var gCountSuccess = 0;
function spawnWorker() {}
var workerStats = {};

// Incorrect
const site_down = 0;
var countSuccess = 0;
function spawn_worker() {}
```

### Variables
- Use `const` for constants and unchanged references
- Use `var` for function-scoped variables (legacy pattern in this codebase)
- Use `let` for block-scoped variables that change
- Legacy `var` is acceptable for consistency with existing code

### Module Pattern
```javascript
var database = {
    init: function( success ) {
        // implementation
    },

    getNextBatch: function( afterQueryFunction ) {
        // implementation
    }
};

module.exports = database;
```

### Async Patterns
- Use callback-based asynchronous code (not Promises/async-await)
- Callbacks receive results directly; check for error conditions in the callback body
- Use `setTimeout` and `setInterval` for timing

```javascript
// Correct
database.getNextBatch( function( rows ) {
    if ( undefined === rows || 0 === rows.length ) {
        return;
    }
    processRows( rows );
});

setTimeout( function() {
    resetVariables();
    getMoreSites();
}, timeToNextLoop );

setInterval( processQueuedRetries, SECONDS * 5 );

// Incorrect - do not use Promises/async-await
const rows = await database.getNextBatch();
```

### Process Communication (IPC)
- Use `process.send()` with `msgtype` field for IPC messages
- Handle messages via `process.on( 'message', callback )`
- Structure: `{ msgtype: 'message_type', worker_pid: pid, payload: data }`

```javascript
// Sending messages
process.send( {
    msgtype: 'send_work',
    worker_pid: process.pid
} );

process.send( {
    msgtype: 'stats',
    worker_pid: process.pid,
    stats: {
        queueLength: arrCheck.length,
        memoryUsage: process.memoryUsage().rss
    }
} );

// Receiving messages
process.on( 'message', function( msg ) {
    switch ( msg.msgtype ) {
        case 'send_work':
            // handle
            break;
        case 'stats':
            // handle
            break;
    }
} );
```

### Guard Clauses
- Use early returns for invalid conditions
- Check for `undefined`, `null`, and empty arrays before processing

```javascript
if ( ! pid )
    return;
if ( 0 == arrObjects.length )
    return;

// Always check before accessing properties
if ( undefined !== arrWorkers[ count ] ) {
    arrWorkers[ count ].send( message );
}
```

### Comparison Style
- Use loose equality `==` for null/undefined checks against primitives
- Use strict equality `===` for boolean and type-sensitive comparisons
- Yoda conditions are NOT used in this codebase

---

## C++ (Native Addon)

### Formatting
- **Indentation**: Tabs
- **Braces**: Same line as function/control structure
- **Namespace**: `using namespace std;` at file top

### Naming Conventions
- **Member variables**: Prefix with `m_` (e.g., `m_host_name`, `m_response_code`)
- **Constants**: `SCREAMING_SNAKE_CASE` (e.g., `MAX_TCP_BUFFER`, `NET_COMMS_TIMEOUT`)
- **Classes**: `Snake_Case` with capital letters (e.g., `HTTP_Checker`)
- **Methods**: `snake_case` (e.g., `get_response_code`, `parse_host_values`)
- **Parameters**: Prefix with `p_` (e.g., `p_host_name`, `p_port`)

```cpp
// Correct
class HTTP_Checker {
private:
    int m_response_code;
    std::string m_host_name;

public:
    void check( std::string p_host_name, int p_port );
    int get_response_code() { return m_response_code; }
};

// Incorrect
class HttpChecker {
private:
    int responseCode;
    std::string hostName;
};
```

### Header Guards
```cpp
#ifndef __HTTP_CHECKER_H__
#define __HTTP_CHECKER_H__

// content

#endif  //__HTTP_H__
```

### Preprocessor Conditionals
- Always comment `#endif` with the macro name

```cpp
#if DEBUG_MODE
    cerr << "debug output" << endl;
#endif // DEBUG_MODE

#if USE_GETADDRINFO
    // getaddrinfo implementation
#else // USE_GETADDRINFO
    // gethostbyname fallback
#endif // USE_GETADDRINFO
```

### Memory Management
- Raw pointers with manual allocation/deallocation
- Clean up in destructor
- Initialize pointers to `NULL` in constructor initializer list

### Error Handling
```cpp
try {
    // risky operation
}
catch( exception &ex ) {
    cerr << "context: " << ex.what() << endl;
}
```

- Log to `cerr` for errors (native addon) or custom `LOG()` macro (veriflier)
- Return early on error conditions
- Set error codes in member variables for caller inspection

---

## C++/Qt (Veriflier)

Same formatting and naming as native addon C++, plus:

### Qt-Specific Patterns
- Use Qt types: `QString`, `QTimer`, `QDateTime`, `QTcpSocket`
- Signals and slots with `Q_OBJECT` macro
- Connect signals: `QObject::connect( sender, SIGNAL( sig() ), receiver, SLOT( slot() ) )`

---

## Shell Scripts

### Shebang
```bash
#!/usr/bin/env bash
```

### Variables
- Reference with braces: `${VARIABLE_NAME}`
- Environment variables in `SCREAMING_SNAKE_CASE`

### Conditionals
```bash
if [ ! -f file.txt ]; then
    # commands
fi
```

### Command Substitution
- Use `$( command )` syntax
- Chain related commands with `&&`

---

## Database Operations

- Use connection pooling via `dbpools.js`
- Always release connections after use
- Use parameterized values for dynamic data

```javascript
pool.cluster.getConnection(
    'MISC_SLAVE*',
    function( err, connection ) {
        if ( err ) {
            logger.error( 'error connecting: ' + err );
            return;
        }
        connection.query(
            sqlQuery,
            function( error, rows ) {
                callBack( error, rows );
                connection.release();  // Always release
            }
        );
    }
);

var query = "UPDATE `jetpack_monitor_sites` " +
    "SET `site_status`=" + Number( site_status ) + ", `last_status_change`=NOW() " +
    "WHERE `blog_id`=" + Number( blog_id );
```

---

## Configuration Access

```javascript
// At module initialization
var config = require( './config' );
config.load();

// Accessing values
var numWorkers = config.get( 'NUM_WORKERS' );
var debugEnabled = config.get( 'DEBUG' );

// For global access in master process
global.config = require( './config' );
```

- Support runtime config reload via SIGHUP signal
- Use constants for magic numbers; define at file top

---

## Logging

Use log4js with appropriate log levels:

```javascript
// Debug information
logger.debug( 'worker thread pid ' + worker.pid + ' shutting down.' );

// Errors
logger.error( 'error connecting to database: ' + err );

// Tracing (for status changes)
slogger.trace( 'status_change: ' + JSON.stringify( server ) );
```

**Logger types:**
- `logger` (flog) - General debug and error logs
- `slogger` (slog) - Status change tracking

**Shutdown note:** During shutdown, use `console.log` instead of logger (logger causes immediate exit):
```javascript
function gracefulShutdown() {
    // Note: calling the 'logger' object during shutdown causes immediate exit
    console.log( 'Caught shutdown signal, disconnecting worker threads.' );
}
```

---

## Error Handling

### Try-Catch Pattern
```javascript
try {
    // risky operation
}
catch ( Exception ) {
    logger.error( 'context: ' + Exception.toString() );
}
```

### Retry Pattern
Retry failed external operations once before giving up:

```javascript
wpcom.notifyStatusChange( server, function( reply ) {
    if ( ! reply.success ) {
        logger.error( 'error, retrying: ' + ( reply?.data || 'no message' ) );
        wpcom.notifyStatusChange( server, function( reply ) {
            if ( ! reply.success ) {
                logger.error( 'error on retry: ' + ( reply?.data || 'no message' ) );
            }
        });
    }
});
```

---

## Metrics & Observability

Emit StatsD metrics for significant events:

```javascript
// Counters
statsdClient.increment( 'worker.spawn.new.count' );
statsdClient.increment( 'stats.sites.total.count', localSitesCount );

// Timers
statsdClient.timing( 'round.complete.time', timeSinceStart );
statsdClient.timing( 'db.get_next_batch', endTime - startTime );

// Naming convention: category.subcategory.metric_name.type
statsdClient.increment( 'worker.check.up.code.200.count', count );
```

- Use consistent metric naming: `category.subcategory.metric_name.type`
- Log status changes to dedicated log files

---

## Compatibility

- Ensure compatibility with Node.js v24
- Use optional chaining for defensive access: `reply?.data`
- C++ must compile with node-gyp on the target Node.js version
- Veriflier requires Qt5 build environment

```javascript
// Modern patterns acceptable in new code
const dataset = get_work_dataset( size );
if ( !dataset || dataset.length === 0 ) {
    return false;
}

// Optional chaining for error handling
logger.error( 'error: ' + ( reply?.data || 'no error message' ) );
```

---

## Development Tools

```bash
# Docker for local development
cd docker && docker compose up -d

# Rebuild native addon after C++ changes
npm run rebuild-run
# Or manually:
node-gyp rebuild && cp build/Release/jetmon.node lib/

# Test configuration changes by reloading
kill -HUP <jetmon-master-pid>
```

---

## Common Pitfalls

- Don't flush retry queues at round start (breaks downtime confirmation)
- Don't overlap bucket ranges between hosts
- Don't exceed memory limits in workers (causes instability)
- Don't use blocking operations in the main event loop
- Don't log sensitive data (auth tokens, credentials)
- Don't modify `arrObjects` while iterating (use splice carefully)
- Always check for `undefined` before accessing properties

---

## Best Practices

### Process Architecture
- Master process manages workers via IPC, not direct function calls
- Workers are disposable; recycle when hitting memory or check limits
- Use exit codes to communicate shutdown reason to master

### Resource Management
- Release database connections after use: `connection.release()`
- Clean up timers and intervals on shutdown
- Track outstanding async operations before exit

---

## Related Documentation

- Documentation standards: `.claude/rules/documentation.md`
- Configuration options: `config/config.readme`
- Docker setup: `docker/` directory
- Veriflier binary: `veriflier2/cmd/main.go`
