{
    'targets':[ {
        'target_name':'jetmon',
        'cflags_cc': [ '-fexceptions','-O3', '-Wno-unused-result' ],
        'sources':[
            './src/main.cpp',
            './src/http_checker.cpp',
        ],
        'conditions': [
        ['node_shared_openssl=="false"', {
          'include_dirs': [
            '<(node_root_dir)/deps/openssl/openssl/include'
          ],
        }]
      ]
    } ]
}
