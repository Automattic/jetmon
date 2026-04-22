# Documentation Guide

Documentation conventions for the Jetmon codebase.

## JavaScript Comments

### JSDoc for Functions
```javascript
/**
 * Brief description of what the function does.
 *
 * @param type name Description of the parameter.
 * @param int|null size Optional parameter with multiple types.
 * @returns {type} Description of return value.
 */
function functionName( param, size = null ) {
    // implementation
}
```

### Variable Documentation
```javascript
/**
 * How many checks are currently being processed by the worker.
 * @type {number}
 */
var activeChecks = 0;
```

### Section and Inline Comments
```javascript
/**
 * Worker asked for work - section comment for logical blocks
 */

var tmpWorkers = freeWorkers;  // inline: take pointer
freeWorkers = [];              // and reset

// TODO: Deprecated. Leave temporarily to track changes.

// Note: calling 'logger' during shutdown causes immediate exit (use console.log)
```

## C++ Comments

### Inline Comments
```cpp
// if we have been redirected, get the details and make a recursive call
if ( ( 300 < m_response_code ) && ( 400 > m_response_code ) ) {

// keep a copy for relative location redirects
string hostname_backup = m_host_name;
```

### Preprocessor Comments
Always comment `#endif` with the macro name:
```cpp
#endif  //__HTTP_H__
#endif // DEBUG_MODE

#if USE_GETADDRINFO
    // implementation
#else // USE_GETADDRINFO
    // fallback
#endif // USE_GETADDRINFO
```

### Header File Options
```cpp
// Enables the printing of debug messages to stderr
#define DEBUG_MODE          0

// getaddrinfo is slower than gethostbyname - only enable with CPU headroom
#define USE_GETADDRINFO     1
```

## Configuration Documentation

Use plain text format in `config/config.readme`:
```
SETTING_NAME
Description of what the setting does. Include default values and valid ranges.

DANGEROUS_SETTING
WARNING: Do not enable in production.
Explanation of when this should or should not be enabled.
```

## README Structure

For main README, use this structure with `====` underlines (not `#` headers):
1. Title
2. Overview
3. Architecture (diagram)
4. Installation (numbered with `)`)
5. Configuration
6. Running
7. Database (schema if applicable)

For component READMEs (e.g., a future `veriflier2/README.md`), use minimal format:
```markdown
component name
==============

Overview
--------
Brief description.

Building
--------
1) Step one
2) Step two
```

## Key Principles

- **Comments explain "why" not "what"** - Avoid restating what code does
- **No formal API docs** - All documentation is manually maintained
- **No JSDoc for C++** - Use inline comments only
- **Config docs are plain text** - Not Markdown
- **SQL in README** - Indent with tabs for proper rendering
- **Image hosting** - Upload to GitHub issue first to get permanent URL

## When to Update README

Update when:
- Adding configuration options affecting usage
- Changing installation/running process
- Modifying database schema
- Adding architectural components

Do NOT include:
- Internal implementation details
- Debugging information
- Temporary workarounds
- Developer-specific notes (use code comments)
