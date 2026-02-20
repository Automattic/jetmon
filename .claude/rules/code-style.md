# Code Style Guide

This document describes the coding conventions used in the Jetmon codebase.

## JavaScript

### Formatting
- **Indentation**: Tabs (not spaces)
- **Quotes**: Single quotes for strings
- **Semicolons**: Required
- **Spaces inside parentheses**: `function( arg )`, `require( 'module' )`, `if ( condition )`
- **Spaces inside brackets**: `arr[ index ]`, `obj[ key ]`
- **Braces**: Same line as control structure

### Naming Conventions
- **Constants**: `SCREAMING_SNAKE_CASE` at file top
- **Global variables**: Prefix with `g` (e.g., `gCountSuccess`, `gCountError`)
- **Local variables**: `camelCase`
- **Functions**: `camelCase` for regular functions, `PascalCase` for constructor-like
- **Module objects**: `camelCase` (e.g., `statsdClient`, `database`)

### Variables
- Use `const` for constants and unchanged references
- Use `var` for function-scoped variables (legacy pattern in this codebase)
- Use `let` for block-scoped variables that change

### Module Pattern
```javascript
var moduleName = {
    methodName: function( param ) {
        // implementation
    }
};

module.exports = moduleName;
```

### Async Patterns
- Callback-based asynchronous code (not Promises/async-await)
- Callbacks receive results directly; check for error conditions in the callback body
- Use `setTimeout` and `setInterval` for timing

### Process Communication
- Use `process.send()` with `msgtype` field for IPC messages
- Handle messages via `process.on( 'message', callback )`
- Structure: `{ msgtype: 'message_type', worker_pid: pid, payload: data }`

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

### Header Guards
```cpp
#ifndef __HTTP_CHECKER_H__
#define __HTTP_CHECKER_H__

// content

#endif  //__HTTP_H__
```

### Preprocessor Conditionals for Debug
```cpp
#if DEBUG_MODE
    cerr << "debug message" << endl;
#endif
```

### Memory Management
- Raw pointers with manual allocation/deallocation
- Clean up in destructor
- Initialize pointers to `NULL` in constructor initializer list

## C++/Qt (Veriflier)

### Formatting
Same as native addon C++ style.

### Qt-Specific Patterns
- Use Qt types: `QString`, `QTimer`, `QDateTime`, `QTcpSocket`
- Signals and slots with `Q_OBJECT` macro
- Connect signals: `QObject::connect( sender, SIGNAL( sig() ), receiver, SLOT( slot() ) )`

### Naming Conventions
- Same `m_` prefix for member variables
- Same `snake_case` for methods
- Qt signal/slot methods follow same convention

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

## Error Handling Standards

### JavaScript
- Wrap risky operations in try-catch blocks
- Log exceptions with `.toString()`: `Exception.toString()`
- Use `logger.error()` for error conditions, `logger.debug()` for diagnostics
- Check for `undefined` explicitly: `if ( undefined !== value )`
- Graceful degradation with fallbacks (e.g., database failover)

```javascript
try {
    // risky operation
}
catch ( Exception ) {
    logger.error( 'context message: ' + Exception.toString() );
}
```

### C++
- Use `try-catch` with `exception &ex`
- Log to `cerr` for errors (native addon) or custom `LOG()` macro (veriflier)
- Return early on error conditions
- Set error codes in member variables for caller inspection

```cpp
try {
    // risky operation
}
catch( exception &ex ) {
    cerr << "context: " << ex.what() << endl;
}
```

### Callback Error Handling
- Check success/error conditions in callback body
- Retry failed operations once before giving up
- Log both initial failure and retry outcome

```javascript
wpcom.notifyStatusChange( server, function( reply ) {
    if ( ! reply.success ) {
        logger.error( 'error posting, retrying: ' + ( reply?.data || 'no error message' ) );
        wpcom.notifyStatusChange( server, function( reply ) {
            if ( ! reply.success )
                logger.error( 'error posting: ' + ( reply?.data || 'no error message' ) );
        });
    }
});
```

## Best Practices

### Process Architecture
- Master process manages workers via IPC, not direct function calls
- Workers are disposable; recycle when hitting memory or check limits
- Use exit codes to communicate shutdown reason to master

### Configuration
- Load configuration from JSON files via `config.load()` / `config.get( 'KEY' )`
- Support runtime config reload via SIGHUP signal
- Use constants for magic numbers; define at file top

### Observability
- Emit StatsD metrics for all significant events
- Use consistent metric naming: `category.subcategory.metric_name.type`
- Log status changes to dedicated log files

### Resource Management
- Release database connections after use: `connection.release()`
- Clean up timers and intervals on shutdown
- Track outstanding async operations before exit

### Guard Clauses
- Use early returns for invalid conditions
- Check for `undefined`, `null`, and empty arrays before processing

```javascript
if ( ! pid )
    return;
if ( 0 == arrObjects.length )
    return;
```

### Comparison Style
- Use loose equality `==` for null/undefined checks against primitives
- Use strict equality `===` for boolean and type-sensitive comparisons
- Yoda conditions are NOT used in this codebase
