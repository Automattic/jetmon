# Rebuild Native Addon

Rebuild the C++ native addon after making changes to `src/http_checker.cpp` or related C++ files.

## Instructions

When the user has modified C++ code and needs to rebuild the native addon, follow these steps:

### 1. Check What Changed
First, identify what C++ files were modified:
```bash
git -C /Users/rdcoll/Code/a8c/jetmon status --porcelain | grep -E '\.(cpp|h|gyp)$'
```

### 2. Determine Build Environment

Ask the user: **Are you running in Docker or locally?**

### 3a. Docker Build (Recommended)

Check if Docker is running:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose ps
```

If not running, start it:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose up -d
```

Rebuild and restart inside container:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon npm run rebuild-run
```

Or if you want to rebuild without auto-running:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon sh -c 'node-gyp rebuild && cp build/Release/jetmon.node lib/'
```

Then restart Jetmon:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose restart jetmon
```

### 3b. Local Build

Run the npm script:
```bash
cd /Users/rdcoll/Code/a8c/jetmon && npm run rebuild-run
```

Or manually:
```bash
cd /Users/rdcoll/Code/a8c/jetmon && node-gyp rebuild && cp build/Release/jetmon.node lib/
```

### 4. Verify Build Success

Check that the new `.node` file was created:
```bash
ls -la /Users/rdcoll/Code/a8c/jetmon/lib/jetmon.node
```

### 5. Test the Addon

Create a quick test to verify the addon loads correctly:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon node -e "const c = require('./lib/jetmon.node'); console.log('Addon loaded successfully');"
```

Or run a simple HTTP check:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon node -e "
const checker = require('./lib/jetmon.node');
checker.http_check('https://wordpress.com', 443, 0, function(idx, rtt, http, err) {
    console.log('RTT:', rtt, 'HTTP:', http, 'Error:', err);
    process.exit(0);
});
"
```

### 6. Watch for Issues

If the build fails, common issues include:
- Missing build tools: `node-gyp` requires Python and a C++ compiler
- Node.js version mismatch: Addon must be built for the running Node.js version
- OpenSSL issues: Check that OpenSSL dev headers are available

If Jetmon crashes after rebuild:
- Check logs: `docker compose logs jetmon`
- Verify the addon API hasn't changed incompatibly
