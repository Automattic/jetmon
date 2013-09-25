{
	'targets':[ {
		'target_name':'watcher',
		'cflags_cc': [ '-fexceptions','-O3' ],
		'sources':[
			'./src/main.cpp',
			'./src/ping.cpp',
			'./src/http_checker.cpp',
		]
	}]
}
