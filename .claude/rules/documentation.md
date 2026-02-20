# Documentation Guide

This document describes the documentation conventions used in the Jetmon codebase.

## Code Documentation

### JavaScript: JSDoc Comments

Use JSDoc-style block comments for functions that have parameters or return values:

```javascript
/**
 * Brief description of what the function does.
 *
 * Additional context or explanation if needed.
 *
 * @param type name Description of the parameter.
 * @param int|null size Optional parameter with multiple types.
 * @returns {type} Description of return value.
 */
function functionName( param, size = null ) {
    // implementation
}
```

For simple getters or self-explanatory functions, a brief JSDoc is sufficient:

```javascript
/**
 * Returns how long the worker has been running.
 *
 * @returns {number}
 */
getAge: function() {
    return Date.now() - createdTime;
}
```

### JavaScript: Variable Documentation

Document variables with non-obvious purposes using JSDoc `@type`:

```javascript
/**
 * How many checks are currently being processed by the worker.
 *
 * @type {number}
 */
var activeChecks = 0;
```

For related variables, use a single block comment:

```javascript
/**
 * The MTU of the network connection that sends StatsD metrics is used to
 * determine the max buffer size.
 */
let statsdMTU = 65536;
```

### JavaScript: Section Comments

Use block comments to introduce logical sections of code:

```javascript
/**
 * Worker asked for work
 */

/**
 * There are no URLs in the global queue, let's flag the worker as "free"
 * and request more sites from the database, if we haven't done so yet.
 */
```

### JavaScript: Inline Comments

Use `//` for brief explanations on the same line or line above:

```javascript
var tmpWorkers = freeWorkers;  // take pointer
freeWorkers = [];              // and reset

// Make sure that we don't give too little or too much work.
if ( !size || size < 1 || size > global.config.get( 'DATASET_SIZE' ) ) {
```

### JavaScript: TODO Comments

```javascript
// TODO: Deprecated. Leave this in temporarily to help track changes
// from the old calculation to the new calculation.
```

### JavaScript: Warning Comments

For dangerous or non-obvious behavior:

```javascript
// Note: calling the 'logger' object during shutdown causes an immediate exit (only use 'console.log')
```

### C++: Inline Comments

Use `//` comments for brief explanations:

```cpp
// if we have been redirected, get the details and make a recursive call
if ( ( 300 < m_response_code ) && ( 400 > m_response_code ) ) {

// keep a copy for relative location redirects
string hostname_backup = m_host_name;

// recalc since we've erased some characters
s_pos = m_host_name.find_first_of( '/' );
```

### C++: Preprocessor Comments

Always comment `#endif` with the macro name:

```cpp
#endif  //__HTTP_H__
#endif // NON_BLOCKING_IO
#endif // USE_GETADDRINFO
```

For conditional compilation blocks, comment both the condition and close:

```cpp
#if NON_BLOCKING_IO // NON_BLOCKING_IO
    // code
#endif // NON_BLOCKING_IO

#else // USE_GETADDRINFO
    // alternative code
#endif // USE_GETADDRINFO
```

### C++: Header File Comments

Document compile-time options with brief explanations:

```cpp
// Enables the printing of debug messages to stderr
#define DEBUG_MODE          0

// getaddrinfo is much slower than gethostbyname and, although
// it is technically the best way to lookup hosts, only enable
// this on hosts with more than enough CPU compute headroom.
#define USE_GETADDRINFO     1
```

## Feature and Configuration Documentation

### Configuration Files (config.readme style)

Document each configuration option in a plain text file with this format:

```
SETTING_NAME
Description of what the setting does. Explain the behavior clearly.
Include default values and valid ranges where applicable.

ANOTHER_SETTING
WARNING: Include warnings for dangerous settings.
Explain when this should or should not be enabled.
```

Example:
```
WORKER_MAX_MEM_MB
The maximum MB of memory that a worker can consume before it stops accepting work and is scheduled to recycle.
Set to 0 or a negative value to disable recycling workers based on memory usage.

DB_UPDATES_ENABLE
WARNING: Do not enabled this on production hosts. This should only be enabled on local docker test environments and never in production.
Set to true to allow Jetmon to update the jetpack_monitor_sites table.
```

## README Documentation

### Main README Structure

Use this section structure for the main README:

1. **Title** - Project name with `=====` underline
2. **Overview** - Brief description of what the project does
3. **Architecture** - Diagram and component descriptions
4. **Installation** - Numbered steps to set up
5. **Configuration** - Where config lives and how to modify
6. **Running** - How to start the service
7. **Database** - Schema definitions if applicable

### README Formatting

**Title and Section Headers:**
```markdown
Project Name
============

Overview
--------
```

**Architecture Diagrams:**
Include via GitHub image URLs:
```markdown
![diagram_name](https://user-images.githubusercontent.com/...)
```

**Status/Flow Tables:**
```markdown
| Previous Status  | Current status   | Action                          |
| ---------------- | ---------------- | ------------------------------- |
| DOWN             | UP               | Notify WPCOM about status change|
```

**Installation Steps:**
Use numbered lists:
```markdown
1) First step description

2) Second step description

3) Third step description
```

**Data Structure Documentation:**
Use nested bullet lists:
```markdown
- `field_name`: Description of the field
    - `nested_field`: Description with enum values: `0` is X, `1` is Y.
```

**Code Blocks:**
Use triple backticks with no language specifier for SQL:
```markdown
	CREATE TABLE `table_name` (
	    `column` type NOT NULL,
	);
```

### Component README Files

For subdirectories (e.g., veriflier/README.md), use a minimal format:

```markdown
component name
==============

Overview
--------
Brief description of what this component does.

Building
--------
1) Step one
2) Step two

Running
-------
1) Step one
2) Step two
```

## Updating the README

### When to Update

Update the main README when:
- Adding new configuration options that affect usage
- Changing the installation or running process
- Modifying the database schema
- Adding new architectural components
- Changing the notification data structure

### What NOT to Include

- Internal implementation details
- Debugging information
- Temporary workarounds
- Developer-specific notes (use code comments instead)

### Maintaining Consistency

- Keep the existing section structure
- Match the formatting style (underlines, not `#` headers)
- Update tables when adding new status flows
- Keep installation steps numbered with `)` not `.`

## Important Notes

- **No formal API documentation**: This codebase does not use automated documentation generators. All documentation is manually maintained.

- **Comments explain "why" not "what"**: Code comments should explain reasoning, edge cases, and non-obvious behavior. Avoid comments that simply restate what the code does.

- **Commented-out code**: Use `//` to disable code temporarily. Include a brief note explaining why it's disabled:
  ```javascript
  // HttpChecker.sendStats();  // Disabled: may cause memory leak before exit
  ```

- **Historical context**: Include historical context in comments when relevant:
  ```
  The following comment was in the worker source code from an early dev on why they chose 45MB as the original value. Since then, we moved to a value of 53MB.
  ```

- **Config documentation is plain text**: The `config/config.readme` file uses plain text, not Markdown. Keep this format when adding new configuration options.

- **Database schema in README**: SQL schema should be indented with tabs in the README for proper rendering.

- **No JSDoc for C++**: The C++ code does not use Doxygen or similar documentation generators. Use inline comments only.

- **Image hosting**: Architecture diagrams are hosted on GitHub's user-content CDN. When adding new diagrams, upload to a GitHub issue first to get a permanent URL.
