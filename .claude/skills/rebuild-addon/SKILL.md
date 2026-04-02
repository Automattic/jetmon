---
name: rebuild-addon
description: Rebuild the C++ native addon after making changes to http_checker.cpp
allowed-tools: Bash(npm run*), Bash(node-gyp*), Bash(docker*), Bash(cp*), Bash(ls*), Read, Glob, Grep
---

# Rebuild Native Addon

Use this skill after making changes to the C++ native addon (`src/http_checker.cpp` or `src/http_checker.h`).

## Usage

- `/rebuild-addon` - Rebuild the addon and restart Jetmon
- `/rebuild-addon docker` - Rebuild inside Docker container
- `/rebuild-addon test` - Rebuild and run a quick test

## Quick Reference

### Using npm Script (Recommended)

```bash
npm run rebuild-run
```

This runs `node-gyp rebuild`, copies the addon to `lib/`, and starts Jetmon.

### Manual Build

```bash
node-gyp rebuild
cp build/Release/jetmon.node lib/
node lib/jetmon.js
```

### Docker Build

```bash
docker compose exec jetmon npm run rebuild-run
```

Or manually inside the container:

```bash
docker compose exec jetmon bash
cd /jetmon
node-gyp rebuild
cp build/Release/jetmon.node lib/
node lib/jetmon.js
```

## Build Verification

After building, verify the addon loads correctly:

```bash
node -e "require('./lib/jetmon.node'); console.log('Addon loaded successfully');"
```

## Testing the Addon

### Quick HTTP Check Test

Create a test script:

```javascript
// lib/test-addon.js
var checker = require( './jetmon.node' );

checker.http_check( 'https://wordpress.com', 80, 0, function( index, rtt, http_code, error_code ) {
    console.log( 'Index:', index );
    console.log( 'RTT (microseconds):', rtt );
    console.log( 'HTTP Code:', http_code );
    console.log( 'Error Code:', error_code );
    process.exit( 0 );
});
```

Run it:
```bash
node lib/test-addon.js
```

### Expected Output

- `index`: The index passed to the check (0 in this case)
- `rtt`: Round-trip time in microseconds
- `http_code`: HTTP response code (200 for success)
- `error_code`: 0 for success, non-zero for errors

### Error Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Connection failed |
| 2 | Timeout |
| 3 | SSL error |
| 4 | DNS resolution failed |
| 5 | Too many redirects |

## C++ Source Files

| File | Purpose |
|------|---------|
| `src/http_checker.cpp` | Main HTTP checking implementation |
| `src/http_checker.h` | Header with class definition |
| `binding.gyp` | Node-gyp build configuration |

## Common Issues

### Build Errors

**Missing OpenSSL headers:**
```
fatal error: openssl/ssl.h: No such file or directory
```
Solution: Install OpenSSL development package:
```bash
# macOS
brew install openssl

# Ubuntu/Debian
apt-get install libssl-dev
```

**Node version mismatch:**
If you see ABI version errors, clean and rebuild:
```bash
node-gyp clean
node-gyp rebuild
```

### Runtime Errors

**Addon not found:**
```
Error: Cannot find module './jetmon.node'
```
Solution: Copy the built addon:
```bash
cp build/Release/jetmon.node lib/
```

**Symbol errors:**
Usually indicates Node.js version changed. Rebuild the addon.

## Debugging C++ Code

### Enable Debug Output

In `src/http_checker.cpp`, set:
```cpp
#define DEBUG_MODE 1
```

Debug output goes to stderr.

### Memory Debugging

For memory leaks, use Valgrind (Linux):
```bash
valgrind --leak-check=full node lib/jetmon.js
```

## Build Configuration

The `binding.gyp` file configures the build:

```json
{
  "targets": [{
    "target_name": "jetmon",
    "sources": ["src/http_checker.cpp"],
    "include_dirs": ["<!(node -e \"require('nan')\")"],
    "libraries": ["-lssl", "-lcrypto"]
  }]
}
```

Key settings:
- Uses NAN (Native Abstractions for Node.js) for compatibility
- Links against OpenSSL for HTTPS support

## After Rebuilding

1. **Test the addon** with a simple HTTP check
2. **Start Jetmon** and verify workers spawn correctly
3. **Monitor logs** for any C++ errors
4. **Check memory usage** to ensure no new leaks
