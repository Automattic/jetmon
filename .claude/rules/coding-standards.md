# Coding Standards

## Priority Order
- Follow coding standards in this order:
    1. Existing patterns in the codebase
    2. Conventions documented in `code-style.md`
    3. Node.js best practices (for JavaScript)
    4. Google C++ Style Guide (for C++, with local modifications)

## JavaScript: Naming & Structure
- Use proper naming conventions
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
- Use object literal modules with `module.exports`
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

## JavaScript: Spacing
- Include spaces inside parentheses and brackets
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

## JavaScript: Async Patterns
- Use callback-based asynchronous code
    ```javascript
    // Correct
    database.getNextBatch( function( rows ) {
        if ( undefined === rows || 0 === rows.length ) {
            return;
        }
        processRows( rows );
    });

    // Incorrect - do not use Promises/async-await
    const rows = await database.getNextBatch();
    ```
- Use `setTimeout` and `setInterval` for timing
    ```javascript
    setTimeout( function() {
        resetVariables();
        getMoreSites();
    }, timeToNextLoop );

    setInterval( processQueuedRetries, SECONDS * 5 );
    ```

## JavaScript: Process Communication
- Structure IPC messages with `msgtype` field
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

## C++: Naming & Structure
- Use proper prefixes and naming conventions
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
- Use header guards with double underscores
    ```cpp
    #ifndef __HTTP_CHECKER_H__
    #define __HTTP_CHECKER_H__

    // content

    #endif  //__HTTP_H__
    ```

## C++: Preprocessor Conditionals
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

## Database Operations
- Use connection pooling via `dbpools.js`
- Always release connections after use
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
    ```
- Use parameterized values for dynamic data
    ```javascript
    var query = "UPDATE `jetpack_monitor_sites` " +
        "SET `site_status`=" + Number( site_status ) + ", `last_status_change`=NOW() " +
        "WHERE `blog_id`=" + Number( blog_id );
    ```

## Configuration Access
- Load configuration via the config module
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

## Metrics & Observability
- Emit StatsD metrics for significant events
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

## Compatibility
- Ensure compatibility with Node.js v24
    - Use `const` and `let` for new code where appropriate
    - Legacy `var` is acceptable for consistency with existing code
    - Use optional chaining for defensive access: `reply?.data`
    ```javascript
    // Modern patterns acceptable in new code
    const dataset = get_work_dataset( size );
    if ( !dataset || dataset.length === 0 ) {
        return false;
    }

    // Optional chaining for error handling
    logger.error( 'error: ' + ( reply?.data || 'no error message' ) );
    ```
- C++ must compile with node-gyp on the target Node.js version
- Veriflier requires Qt5 build environment

## Development Tools
- Use Docker for local development:
    ```bash
    cd docker && docker compose up -d
    ```
- Rebuild native addon after C++ changes:
    ```bash
    npm run rebuild-run
    # Or manually:
    node-gyp rebuild && cp build/Release/jetmon.node lib/
    ```
- Test configuration changes by reloading:
    ```bash
    kill -HUP <jetmon-master-pid>
    ```

## Common Pitfalls
- Don't flush retry queues at round start (breaks downtime confirmation)
- Don't overlap bucket ranges between hosts
- Don't exceed memory limits in workers (causes instability)
- Don't use blocking operations in the main event loop
- Don't log sensitive data (auth tokens, credentials)
- Don't modify `arrObjects` while iterating (use splice carefully)
- Always check for `undefined` before accessing properties
    ```javascript
    // Correct
    if ( undefined !== arrWorkers[ count ] ) {
        arrWorkers[ count ].send( message );
    }

    // Incorrect
    arrWorkers[ count ].send( message );  // May crash
    ```

## Logging
- Use log4js with appropriate log levels
    ```javascript
    // Debug information
    logger.debug( 'worker thread pid ' + worker.pid + ' shutting down.' );

    // Errors
    logger.error( 'error connecting to database: ' + err );

    // Tracing (for status changes)
    slogger.trace( 'status_change: ' + JSON.stringify( server ) );
    ```
- Use separate loggers for different purposes:
    - `logger` (flog) - General debug and error logs
    - `slogger` (slog) - Status change tracking
- During shutdown, use `console.log` instead of logger (logger causes immediate exit)
    ```javascript
    function gracefulShutdown() {
        // Note: calling the 'logger' object during shutdown causes immediate exit
        console.log( 'Caught shutdown signal, disconnecting worker threads.' );
    }
    ```

## Error Handling
- Wrap risky operations in try-catch
    ```javascript
    try {
        // risky operation
    }
    catch ( Exception ) {
        logger.error( 'context: ' + Exception.toString() );
    }
    ```
- Retry failed external operations once
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

## Related Documentation
- Code style details: `.claude/rules/code-style.md`
- Documentation standards: `.claude/rules/documentation.md`
- Configuration options: `config/config.readme`
- Docker setup: `docker/` directory
- Veriflier build: `veriflier/README.md`
