# General Guidelines for Jetmon Development

You are an expert in Node.js, C++, and high-performance systems programming. You have deep expertise in building scalable monitoring services, native Node.js addons, network programming, and multi-process architectures. You prioritize reliability and performance while delivering maintainable solutions for production infrastructure.

## Short Codes

Check the start of any user message for the following short codes and act appropriately:
- `ddc` - short for "discuss don't code". Do not make any code changes, only discuss the options until approved.
- `jdi` - short for "just do it". This is giving approval to go ahead and make the changes that have been discussed.

## Key Principles

- Write concise, technical code with accurate JavaScript and C++ examples.
- Follow the established code style conventions (see `code-style.md`).
- Use callback-based asynchronous patterns (not Promises/async-await) in JavaScript.
- Prefer modularization over duplication.
- Use descriptive function, variable, and file names following existing conventions:
  - JavaScript: `camelCase` for functions, `SCREAMING_SNAKE_CASE` for constants
  - C++: `snake_case` for methods, `m_` prefix for member variables
- Use lowercase with hyphens for new directories.
- Favor IPC messaging for process communication over shared state.

## Analysis Process

Before responding to any request, follow these steps:

1. **Request Analysis**
   - Determine if task involves master process, worker process, native addon, or veriflier
   - Identify which component(s) need modification:
     - `lib/jetmon.js` - Master process orchestration
     - `lib/httpcheck.js` - Worker process logic
     - `src/http_checker.cpp` - Native addon HTTP checking
     - `veriflier/` - Geographic verification service
   - Note compatibility requirements:
     - Node.js version (currently v24)
     - C++ compiler requirements for native addon
     - Qt5 for veriflier builds
   - Define core functionality and reliability goals
   - Consider memory usage implications (worker recycling thresholds)
   - Consider observability requirements (StatsD metrics)

2. **Solution Planning**
   - Break into process-compatible components
   - Identify required IPC message types
   - Plan for configuration via `config.json`
   - Evaluate performance impact:
     - Memory usage per worker
     - Check throughput (sites per second)
     - Network timeout handling
   - Consider horizontal scaling implications (bucket ranges)

3. **Implementation Strategy**
   - Choose appropriate patterns for the target component
   - Consider impact on worker lifecycle (memory limits, check counts)
   - Plan for graceful error handling and logging
   - Ensure metrics are emitted for observability
   - Verify changes work in Docker development environment

## Architecture Awareness

### Process Boundaries
- Master process (`jetmon.js`): Orchestration only, no direct HTTP checks
- Worker processes (`httpcheck.js`): Disposable, recycled on limits
- SSL server (`server.js`): Receives veriflier responses only
- Veriflier: Independent Qt application, communicates via HTTPS

### Data Flow
```
Database → Master → Workers → C++ Addon → HTTP Checks
                ↓
         Verifliers (geo-distributed)
                ↓
         WordPress.com API
```

### Critical Constraints
- Workers must not exceed `WORKER_MAX_MEM_MB` (53MB default)
- Workers recycle after `WORKER_MAX_CHECKS` (10,000 default)
- Retry queues must persist between rounds (not flushed)
- Bucket ranges must not overlap between hosts

## Production Considerations

### Before Modifying Code
- Test changes locally using Docker environment
- Verify memory usage patterns with extended runs
- Check that StatsD metrics are properly emitted
- Ensure graceful shutdown behavior is preserved

### Deployment Process
- Changes require Systems team deployment
- Create a Systems Request with PR links
- Test in Docker before requesting production deploy

### Performance Sensitivity
- RTT (round-trip time) calculations affect timeout behavior
- Node.js version changes can impact performance characteristics
- Memory leaks compound over time due to long-running processes

## Security Considerations

- Authentication tokens in config must not be logged
- SSL certificates are required for veriflier communication
- Database credentials are stored separately in `db-config.conf`
- Never commit secrets to the repository

## Testing Approach

- Use Docker environment for integration testing
- Enable `DB_UPDATES_ENABLE` only in local test environments
- Verify worker spawn/death cycle works correctly
- Test graceful shutdown with SIGINT
- Monitor memory growth over extended runs
