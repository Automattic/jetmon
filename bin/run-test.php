<?php


$run_dir = getcwd();
$project_dir = dirname( __DIR__ );


$default_args = array(
	'default-config-file' => "$project_dir/config/config.json",
	'configs-dir'         => '',
//	'configs-script'      => '',
	'help'                => '',
	'run-time'            => 300,
	'verbose'             => false,
);
$args = array_merge( $default_args, get_run_args() );


if ( preg_match( '/permission denied/', `docker ps 2>&1` ) ) {
	fwrite( STDERR, "Permission denied. Please run the script with sudo.\n" );
	exit( 1 );
}

pcntl_signal( SIGTERM, 'sigterm_handler' );
pcntl_signal( SIGINT,  'sigterm_handler' );

if ( are_containers_running() ) {
	fwrite( STDERR, 'Error: The containers are already running. ' );
	if ( confirm_action( 'Should this script stop the containers?' ) ) {
		stop_containers();
	} else {
		exit( 1 );
	}
}


$configs = get_configs();

if ( empty( $configs ) ) {
	$configs['default'] = false;
}

foreach ( $configs as $config_name => $config ) {
	backup_config();
	update_config( $config );

	start_containers();

	show_verbose_message( "Waiting {$args['run-time']} seconds." );
	sleep( $args['run-time'] );

	$stats = get_stats_from_logs();
	//echo json_encode( $stats );
	echo "Config: $config_name\n";
	echo $stats['totals']['ms']['round.complete.time'] . "\n";

	restore_config();
	stop_containers();
}



function update_config( $config ) {
	global $project_dir;

	show_verbose_message( 'Updating config.' );
	file_put_contents( "$project_dir/config/config.json", $config );
}

function get_configs() {
	global $run_dir, $args;

	$cwd = getcwd();
	chdir( $run_dir );

	$default_config = json_decode( trim( file_get_contents( $args['default-config-file'] ) ), true );
	$configs = array();

	if ( ! empty( $args['configs-dir'] ) ) {
		show_verbose_message( "Getting configs from directory: {$args['configs-dir']}" );
		if ( ! is_dir( $args['configs-dir'] ) ) {
			fwrite( STDERR, "Unable to find supplied configs dir.\n" );
			exit( 1 );
		}

		$files = glob( "{$args['configs-dir']}/*.json" );

		foreach ( $files as $file ) {
			$config = json_decode( trim( file_get_contents( $file ) ), true );
			$config['OUTPUT_STATS_TO_CONSOLE'] = true;

			$configs[$file] = json_encode( array_merge( $default_config, $config ) );
		}
	}

	chdir( $cwd );
	return $configs;
}

function show_verbose_message( $message ) {
	global $args;

	if ( $args['verbose'] ) {
		echo "$message\n";
	}
}

function sigterm_handler() {
	fwrite( STDERR, 'Exiting...' );
	restore_config();
	stop_containers();
	fwrite( STDERR, "\n" );
}

function backup_config() {
	global $project_dir, $backup_config_file;

	if ( empty( $backup_config_file ) ) {
		show_verbose_message( 'Creating backup config file.' );
		$backup_config_file = tempnam( "$project_dir/config", 'config.json.' );
		copy( "$project_dir/config/config.json", $backup_config_file );
		chmod( $backup_config_file, fileperms( "$project_dir/config/config.json" ) );
		chown( $backup_config_file, fileowner( "$project_dir/config/config.json" ) );
		chgrp( $backup_config_file, filegroup( "$project_dir/config/config.json" ) );
	}
}

function restore_config() {
	global $project_dir, $backup_config_file;

	if ( ! empty( $backup_config_file ) ) {
		show_verbose_message( 'Restoring backed up config file.' );
		copy( $backup_config_file, "$project_dir/config/config.json" );
		unlink( $backup_config_file );
		$backup_config_file = '';
	}
}

function get_run_args() {
	$shortcuts = array(
		'c' => 'default-config-file',
		'd' => 'configs-dir',
//		's' => 'configs-script',
		'h' => 'help',
		't' => 'run-time',
		'v' => 'verbose',
	);

	$shortopts = 'c::d::s::h::t::v::';
	$longopts  = array(
		'default-config-file',
		'configs-dir::',
//		'configs-script::',
		'help::',
		'run-time::',
		'verbose::',
	);

	$raw_args = getopt( $shortopts, $longopts );
	$args = array();

	foreach ( $shortcuts as $shortcut => $full_name ) {
		if ( isset( $raw_args[$full_name] ) ) {
			$args[$full_name] = $raw_args[$full_name];
		} else if ( isset( $raw_args[$shortcut] ) ) {
			$args[$full_name] = $raw_args[$shortcut];
		}

		if ( isset( $args[$full_name] ) && false === $args[$full_name] ) {
			$args[$full_name] = true;
		}
	}

	if ( ! empty( $args['help'] ) ) {
		show_usage();
	}

	return $args;
}

function get_stats_from_logs() {
	show_verbose_message( 'Generating stats.' );

	$raw_logs = trim( `docker logs docker-jetmon-1 2>/dev/null` );
	//file_put_contents( __FILE__ . '.logs', $raw_logs );
	//$raw_logs = file_get_contents( __FILE__ . '.logs' );
	$logs = explode( "\n", $raw_logs );

	$round_data = array();
	$stats = array(
		'rounds' => array(),
		'totals' => array(),
	);

	foreach ( $logs as $key => $log ) {
		$log = trim( $log );

		if ( preg_match( '/^com\.jetpack\.jetmon\.jetmon\.docker\.(.+?):(.+?)\|(.+?)\|/', $log, $match ) ) {
			if ( in_array( $match[3], array( 'c', 'ms' ) ) ) {
				if ( ! isset( $stats['totals'][$match[3]][$match[1]] ) ) {
					$stats['totals'][$match[3]][$match[1]] = 0;
				}
				$stats['totals'][$match[3]][$match[1]] += $match[2];

				if ( ! isset( $round_data[$match[3]][$match[1]] ) ) {
					$round_data[$match[3]][$match[1]] = 0;
				}
				$round_data[$match[3]][$match[1]] += $match[2];

				if ( 'ms' === $match[3] && 'round.complete.time' === $match[1] ) {
					$stats['rounds'][] = $round_data;
					$round_data = array();
				}
			}
		}
	}

	if ( ! empty( $round_data ) ) {
		$stats['rounds'][] = $round_data;
	}

	return $stats;
}

function stop_containers() {
	global $project_dir;

	show_verbose_message( 'Stopping containers.' );
	chdir( "$project_dir/docker" );
	`docker compose down 2>/dev/null`;
}

function start_containers() {
	global $project_dir;

	show_verbose_message( 'Starting containers.' );
	chdir( "$project_dir/docker" );
	`docker compose up -d 2>/dev/null`;

	// Wait to ensure that the containers have time to spool up.
	sleep( 10 );

	if ( ! are_containers_running() ) {
		fwrite( STDERR, "Error: Not all the expected containers are running. Stopping.\n" );
		exit( 1 );
	}
}

function are_containers_running() {
	show_verbose_message( 'Checking to ensure that the containers are running.' );
	$running_containers = explode( "\n", trim( `docker ps --format '{{.Names}}' 2>&1` ) );

	foreach ( array( 'docker-jetmon-1', 'docker-mysqldb-1', 'docker-veriflier-1', 'docker-statsd-1' ) as $expected_container ) {
		if ( ! in_array( $expected_container, $running_containers ) ) {
			return false;
		}
	}

	return true;
}

function confirm_action( $message ) {
	$input = '';

	while ( ! in_array($input, array( 'n', 'y' ) ) ) {
		fwrite( STDERR, "$message (y/N): " );
		$input = trim( fgets( STDIN ) );
		$input = strtolower( $input );
	}

	return 'y' === $input;
}

function show_usage( $message = '', $exit_code = 0 ) {
	global $argv;

	$script_name = basename( $argv[0] );

	if ( ! empty( $message ) ) {
		echo "$message\n\n";
	}

	fwrite( STDERR, "usage: $script_name [<argument>]...\n\n" );

	fwrite( STDERR, "The following arguments are available:\n" );
	fwrite( STDERR, "   -h --help            Show this help message\n" );
	fwrite( STDERR, "   -v --verbose         Show messages about each step of the tests\n" );
	fwrite( STDERR, "   -t --run-time        Number of seconds to run each config for\n" );
	fwrite( STDERR, "   -d --configs-dir     Directory to pull configs to loop through\n" );
//	fwrite( STDERR, "   -d --configs-script  Script to get configs from to loop through\n" );

	exit( $exit_code );
}
