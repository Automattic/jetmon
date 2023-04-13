
#include "http_checker.h"
#include <cstring>

using namespace std;

const int ERROR_STATUS_CODE_UNKNOWN = 999;
const int ERROR_TIMEOUT = 998;
const int ERROR_REDIRECT_LOCATION = 997;
const int ERROR_CONNECT_REDIRECT_HOST = 996;
const int ERROR_CONNECT_HOST = 995;

HTTP_Checker::HTTP_Checker() : m_sock( -1 ), m_host_name( "" ), m_host_dir( "" ), m_port( HTTP_DEFAULT_PORT ),
		m_is_ssl( false ), m_triptime( 0 ), m_response_code( 0 ), m_ctx( NULL ), m_ssl( NULL ), m_sbio( NULL ) {
	gettimeofday( &m_tstart, &m_tzone );
	memset( m_buf, 0, MAX_TCP_BUFFER );
	m_cutofftime = time( NULL );
	m_cutofftime += NET_COMMS_TIMEOUT;
}

HTTP_Checker::~HTTP_Checker() {
	this->disconnect();
}

time_t HTTP_Checker::get_rtt() {
	struct timeval m_tend;
	gettimeofday( &m_tend, &m_tzone );

	if ( (m_tend.tv_usec -= m_tstart.tv_usec) < 0 ) {
		m_tend.tv_sec--;
		m_tend.tv_usec += 1000000;
	}
	m_tend.tv_sec -= m_tstart.tv_sec;
	return m_tend.tv_sec * 1000000 + ( m_tend.tv_usec );
}

void HTTP_Checker::check( string p_host_name, int p_port ) {
	try {
		m_host_name = p_host_name;
		m_port = p_port;
		m_host_dir = '/';

		this->parse_host_values();
		if ( connect() ) {
			this->set_host_response( 0 );
		} else {
#if DEBUG_MODE
				cerr << "Unable to connect to host" << endl;
#endif
				m_response_code = ERROR_CONNECT_HOST;
			}
	}
	catch( exception &ex ) {
		cerr << "exception in HTTP_Checker::check(): for host '" << p_host_name.c_str() << "'" << endl;
	}
}

void HTTP_Checker::set_host_response( int redirects ) {
	try {
		string response = this->send_http_get();
		if ( 0 >= response.size() ) {
#if DEBUG_MODE
			cerr << "no response - timed out" << endl;
#endif
			m_response_code = ERROR_TIMEOUT;
			return;
		}

		if ( 8 != response.find_first_of( ' ' ) ) {
#if DEBUG_MODE
			cerr << "Status code unknown" << endl;
#endif
			m_response_code = ERROR_STATUS_CODE_UNKNOWN;
			return;
		}

		string s_response_code = response.substr( 9, 3 );
		m_response_code = atoi( s_response_code.c_str() );

		// if we have been redirected, get the details and make a recursive call
		if ( ( 300 < m_response_code ) && ( 400 > m_response_code ) ) {
			redirects++;
			if ( MAX_REDIRECTS < redirects ) {
#if DEBUG_MODE
				cerr << "Hit max on the redirects" << endl;
#endif
				// Note we leave the 3xx response code so this site is marked as up
				return;
			}
			if ( ! set_redirect_host_values( response ) ) {
#if DEBUG_MODE
				cerr << "Unable to parse redirect location" << endl;
#endif
				m_response_code = ERROR_REDIRECT_LOCATION;
				return;
			}
			this->disconnect();
			if ( this->connect() ) {
				this->set_host_response( redirects );
			} else {
#if DEBUG_MODE
				cerr << "Unable to connect to redirect host" << endl;
#endif
				m_response_code = ERROR_CONNECT_REDIRECT_HOST;
			}
		}

#if DEBUG_MODE
			cerr << m_host_name.c_str() << " : " << m_response_code << endl;
#endif

	}
	catch( exception &ex ) {
		cerr << "exception in HTTP_Checker::set_host_responses(): for host '" << m_host_name.c_str() << "'" << endl;
	}
}

bool HTTP_Checker::set_redirect_host_values( string p_content ) {
	try {
		string p_lcase_search = p_content;

		std::transform( p_lcase_search.begin(), p_lcase_search.end(), p_lcase_search.begin(), ::tolower );

		if ( string::npos == p_lcase_search.find( "location: " ) )
			return false;

		p_content = p_content.substr( p_lcase_search.find( "location: " ) + 10, p_content.length() - ( p_lcase_search.find( "location: " ) + 10 ) );

		if ( string::npos == p_content.find( "\r\n" ) )
			return false;

		p_content.erase( p_content.find_first_of( "\r\n" ), p_content.length() - p_content.find_first_of( "\r\n" ) );

		// keep a copy for relative location redirects
		string hostname_backup = m_host_name;
		m_host_name = p_content;
		m_port = HTTP_DEFAULT_PORT;
		m_host_dir = '/';
		m_is_ssl = false;

		this->parse_host_values();

		// this is a relative location redirect, reinstate hostname
		if ( 0 == m_host_name.size() )
			m_host_name = hostname_backup;

		return true;
	}
	catch( exception &ex ) {
		cerr << "exception in HTTP_Checker::set_redirect_host_values()" << endl;
		return false;
	}
}

void HTTP_Checker::parse_host_values() {
	if ( string::npos != m_host_name.find( "http://" ) ) {
		m_host_name.erase( m_host_name.find( "http://" ), 7 );
	}

	if ( string::npos != m_host_name.find( "https://" ) ) {
		m_host_name.erase( m_host_name.find( "https://" ), 8 );
		m_port = HTTPS_DEFAULT_PORT;
		m_is_ssl = true;
	}

	size_t s_pos = m_host_name.find_first_of( '/' );
	size_t q_pos = m_host_name.find_first_of( '?' );
	size_t c_pos = m_host_name.find_first_of( ':' );
	size_t f_pos = m_host_name.find_first_of( '#' );

	if ( ( c_pos < s_pos ) && ( c_pos < q_pos ) && ( c_pos < f_pos ) ) {
		int new_port = atoi( m_host_name.substr( c_pos + 1, min( s_pos, m_host_name.length() ) ).c_str() );
		if ( 0 < new_port ) {
			m_port = new_port;
			m_host_name.erase( c_pos, min( s_pos, m_host_name.length() ) - c_pos );
			// recalc since we've erased some characters
			s_pos = m_host_name.find_first_of( '/' );
			q_pos = m_host_name.find_first_of( '?' );
			f_pos = m_host_name.find_first_of( '#' );
		}
	}

	if ( string::npos != s_pos || string::npos != q_pos || string::npos != f_pos ) {
		size_t m_pos = min( min( s_pos, q_pos ), f_pos );
		m_host_dir = m_host_name.substr( m_pos, m_host_name.length() - m_pos );
		if ( 0 == m_host_dir.length() || '?' == m_host_dir[0] || '#' == m_host_dir[0] ) {
			m_host_dir = "/" + m_host_dir;
		}
		m_host_name.erase( m_pos, m_host_name.length() - m_pos );
	}
}

string HTTP_Checker::send_http_get() {
	string s_tmp = "HEAD " + m_host_dir + " HTTP/1.1\r\n";
			s_tmp += "Host: " + m_host_name + "\r\n";
			s_tmp += "User-Agent: jetmon/1.0 (Jetpack Site Uptime Monitor by WordPress.com)\r\n";
			s_tmp += "Connection: close\r\n\r\n";

	strcpy( m_buf, s_tmp.c_str() );

	if ( send_bytes( m_buf, s_tmp.length() ) ) {
		s_tmp = get_response();
	} else {
		s_tmp = "";
#if DEBUG_MODE
		cerr << "failed to send_bytes()" << endl;
#endif
	}
	return s_tmp;
}

string HTTP_Checker::get_response() {
	try {
		ssize_t received;
		fd_set read_fds;
		struct timeval tv;
		string ret_val = "";

		do {
			tv.tv_sec = 0;
			tv.tv_usec = 500000;
			FD_ZERO( &read_fds );
			FD_SET( m_sock, &read_fds );

			::select( m_sock + 1, &read_fds, NULL, NULL, &tv );
		} while ( ( FD_ISSET( m_sock, &read_fds ) == 0) && ( m_cutofftime > time( NULL ) ) );

		if ( FD_ISSET( m_sock, &read_fds) ) {
			if ( m_is_ssl )
				received = SSL_read( m_ssl, m_buf, MAX_TCP_BUFFER - 1 );
			else
				received = ::recv( m_sock, m_buf, MAX_TCP_BUFFER - 1, 0 );

			while ( received > 0 ) {
				if ( received < MAX_TCP_BUFFER ) {
					m_buf[ received ] = '\0';
					ret_val += m_buf;
				}
				do
				{
					tv.tv_sec = 0;
					tv.tv_usec = 500000;
					FD_ZERO( &read_fds );
					FD_SET( m_sock, &read_fds );

					select( m_sock + 1, &read_fds, NULL, NULL, &tv );
				} while( (FD_ISSET( m_sock, &read_fds ) == 0) && ( m_cutofftime > time( NULL ) ) );

				if( FD_ISSET( m_sock, &read_fds) )
					if ( m_is_ssl )
						received = SSL_read( m_ssl, m_buf, MAX_TCP_BUFFER - 1 );
					else
						received = ::recv( m_sock, m_buf, MAX_TCP_BUFFER - 1, 0 );
				else
					received = 0;
			}
		}
		return ret_val;
	}
	catch( exception& ex ) {
		cerr << "exception in HTTP_Checker::get_response(): for host '" << m_host_name.c_str() << "'" << endl;
		return "";
	}
}

bool HTTP_Checker::init_socket( addrinfo *addr ) {
	if ( NULL != addr ) {
		m_sock = ::socket( addr->ai_family, addr->ai_socktype, addr->ai_protocol );
	} else {
		m_sock = ::socket( AF_INET, SOCK_STREAM, IPPROTO_TCP );
	}
	if ( -1 == m_sock ) {
		errno = 0;
#if DEBUG_MODE
		cerr << "unable to create socket" << endl;
#endif
		return false;
	}

	int val = 1;
	int ret_val = ::setsockopt( m_sock, SOL_SOCKET, SO_REUSEADDR, &val, sizeof( val ) );
	if( -1 == ret_val ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
#if DEBUG_MODE
		cerr << "unable to set socket option SO_REUSEADDR" << endl;
#endif
		return false;
	}

#if NON_BLOCKING_IO
	int flags = fcntl( m_sock, F_GETFL, 0 );
	if ( fcntl( m_sock, F_SETFL, flags | O_NONBLOCK ) ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
#if DEBUG_MODE
		cerr << "could not fcntl" << endl;
#endif
		return false;
	}
#endif // NON_BLOCKING_IO

	struct timeval time_out;
	time_out.tv_sec = NET_COMMS_TIMEOUT;
	time_out.tv_usec = 0;

	ret_val = ::setsockopt( m_sock, SOL_SOCKET, SO_SNDTIMEO, &time_out, sizeof( time_out ) );
	if( -1 == ret_val ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
#if DEBUG_MODE
		cerr << "unable to set socket option SO_SNDTIMEO" << endl;
#endif
		return false;
	}

	ret_val = ::setsockopt( m_sock, SOL_SOCKET, SO_RCVTIMEO, &time_out, sizeof( time_out ) );
	if( -1 == ret_val ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
#if DEBUG_MODE
		cerr << "unable to set socket option SO_RCVTIMEO" << endl;
#endif
		return false;
	}

	return true;
}

bool HTTP_Checker::init_ssl() {
	m_ctx = SSL_CTX_new( SSLv23_client_method() );

	if ( NULL == m_ctx ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
#if DEBUG_MODE
		cerr << "unable to set SSL context" << endl;
#endif
		return false;
	}

#ifdef SSL_MODE_RELEASE_BUFFERS
	SSL_CTX_set_mode( m_ctx, SSL_MODE_RELEASE_BUFFERS );
#endif

	if ( ! SSL_CTX_load_verify_locations( m_ctx, NULL, "/etc/ssl/certs" ) ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
#if DEBUG_MODE
		cerr << "unable to load the cert location" << endl;
#endif
		return false;
	}

	m_ssl = SSL_new( m_ctx );

	if ( NULL == m_ssl ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
#if DEBUG_MODE
		cerr << "unable to set init SSL" << endl;
#endif
		return false;
	}

	SSL_set_mode( m_ssl, SSL_MODE_AUTO_RETRY );
	return true;
}

#if USE_GETADDRINFO
bool HTTP_Checker::connect_getaddrinfo() {
	try {
		addrinfo *res = 0;
		struct addrinfo hints;
		memset( &hints, 0, sizeof( hints ) );
		hints.ai_family = AF_UNSPEC;
		hints.ai_flags = AI_ADDRCONFIG;
		hints.ai_socktype = SOCK_STREAM;
		int con_ret = -1;
		int result = -1;

#if DEBUG_MODE
		cerr << "getaddrinfo: looking up " << m_host_name.c_str() << endl;
#endif
		string s_lookup_type = "http";
		if ( m_is_ssl ) {
			s_lookup_type = "https";
		}

		result = getaddrinfo( m_host_name.c_str(), s_lookup_type.c_str(), &hints, &res );
		if ( EAI_BADFLAGS == result ) {
			hints.ai_flags = 0;
			result = getaddrinfo( m_host_name.c_str(), s_lookup_type.c_str(), &hints, &res );
		}

		if ( EAI_NONAME == result ) {
#if DEBUG_MODE
			cerr << "NXDOMAIN: " << m_host_name.c_str() << endl;
#endif
			return false;
		}
		if ( 0 != result || EAI_FAIL == result ) {
#if DEBUG_MODE
			cerr << "Error looking up host: " << m_host_name.c_str() << endl;
#endif
			return false;
		}

		addrinfo *node = res;
		int tried_recs = 0;
		while ( node && m_cutofftime > time( NULL ) ) {
			if ( ! ( AF_INET == node->ai_family || AF_INET6 == node->ai_family ) ) {
				node = node->ai_next;
				continue;
			}
			tried_recs++;
			if ( ! init_socket( node ) ) {
#if DEBUG_MODE
				cerr << "socket init failed" << endl;
#endif
				node = node->ai_next;
				continue;
			}

#if NON_BLOCKING_IO // NON_BLOCKING_IO
			struct epoll_event ev;
			struct epoll_event events[MAX_EPOLL_EVENTS];

			int e_fd = epoll_create1( 0 );
			if ( -1 == e_fd ) {
#if DEBUG_MODE
				cerr << "epoll_create failed" << endl;
#endif
				close( m_sock );
				m_sock = -1;
				errno = 0;
				node = node->ai_next;
				continue;
			}

			ev.data.fd = m_sock;
			ev.events = EPOLLOUT | EPOLLIN | EPOLLERR | EPOLLHUP;
			int c_fd = epoll_ctl( e_fd, EPOLL_CTL_ADD, m_sock, &ev );
			if ( 0 != c_fd ) {
#if DEBUG_MODE
				cerr << "epoll_ctl failed" << endl;
#endif
				close( e_fd );
				close( m_sock );
				m_sock = -1;
				errno = 0;
				node = node->ai_next;
				continue;
			}

#endif // NON_BLOCKING_IO

			con_ret = ::connect( m_sock, node->ai_addr, node->ai_addrlen );

#if NON_BLOCKING_IO // NON_BLOCKING_IO

			if ( con_ret < 0 && errno != EINPROGRESS ) {
#if DEBUG_MODE
				cerr << "socket connect failed" << endl;
#endif
				close( e_fd );
				close( m_sock );
				m_sock = -1;
				con_ret = -1;
				errno = 0;
				node = node->ai_next;
				continue;
			}

			if ( con_ret == 0 ) {
				close( e_fd );
				break;
			}

			int timeout = m_cutofftime - time( NULL );
			if ( timeout < 0 ) {
#if DEBUG_MODE
				cerr << "timed out for " << m_host_name.c_str() << endl;
#endif
				errno = 0;
				con_ret = -1;
				close( e_fd );
				break;
			}
			int num_events = epoll_wait( e_fd, events, MAX_EPOLL_EVENTS, timeout * 1000 );
			for ( int i = 0; i < num_events; i++ ) {
				if ( events[i].events & EPOLLERR || events[i].events & EPOLLHUP ) {
#if DEBUG_MODE
					cerr << "epoll error or HUP" << endl;
#endif
					con_ret = -1;
					break;
				} else if ( events[i].events & EPOLLOUT ) {
					con_ret = 0;
					break;
				}
			}
			close( e_fd );
#endif // NON_BLOCKING_IO

			if ( con_ret == 0 ) {
				break;
			}
#if DEBUG_MODE
			cerr << "failed to connect to " << m_host_name.c_str() << endl;
#endif
			close( m_sock );
			m_sock = -1;
			con_ret = -1;
			errno = 0;
			node = node->ai_next;
		}
#if DEBUG_MODE
		if ( 0 == tried_recs && 0 == node ) {
			cerr <<  "unknown address types for: " << m_host_name.c_str() << endl;
		}
#endif
		freeaddrinfo( res );
		return ( 0 == con_ret );
	}
	catch( exception& ex ) {
		cerr << "exception in HTTP_Checker::connect(): for host '" << m_host_name.c_str() << "'" << endl;
		return false;
	}
}

#else // USE_GETADDRINFO


bool HTTP_Checker::connect_gethostbyname() {
	try {
		struct sockaddr_in m_addr;
		char *tmp = (char *)malloc( MAX_TCP_BUFFER );
		struct hostent hostbuf, *hp;
		int herr, hres;

#if DEBUG_MODE
		cerr << "gethostbyname: looking up " << m_host_name.c_str() << endl;
#endif
		hres = gethostbyname_r( m_host_name.c_str(), &hostbuf, tmp, MAX_TCP_BUFFER, &hp, &herr );
		if ( ERANGE == hres ) {
#if DEBUG_MODE
			cerr << "realloc for DNS results" << endl;
#endif
			tmp = (char *)realloc( tmp, ( MAX_TCP_BUFFER * 2 ) );
			if ( NULL == tmp ) {
#if DEBUG_MODE
				cerr << "realloc error!" << endl;
#endif
				return false;
			}
			hres = gethostbyname_r( m_host_name.c_str(), &hostbuf, tmp, ( MAX_TCP_BUFFER * 2 ), &hp, &herr );
		}

		if ( hp ) {
			m_addr.sin_port = htons( m_port );
			m_addr.sin_family = hp->h_addrtype;
			bcopy( hp->h_addr, (caddr_t)&m_addr.sin_addr, hp->h_length );
		} else {
#if DEBUG_MODE
			cerr << "NXDOMAIN: " << m_host_name.c_str() << endl;
#endif
			free( tmp );
			return false;
		}

		if ( ! init_socket( NULL ) ) {
#if DEBUG_MODE
			cerr << "socket init failed" << endl;
#endif
			free( tmp );
			return false;
		}

#if NON_BLOCKING_IO // NON_BLOCKING_IO
		int e_fd = epoll_create1( 0 );
		if ( -1 == e_fd ) {
#if DEBUG_MODE
			cerr << "epoll_create failed" << endl;
#endif
			close( m_sock );
			m_sock = -1;
			errno = 0;
			free( tmp );
			return false;
		}

		struct epoll_event ev;
		struct epoll_event events[MAX_EPOLL_EVENTS];

		ev.data.fd = m_sock;
		ev.events = EPOLLOUT | EPOLLIN | EPOLLERR | EPOLLHUP;
		int c_fd = epoll_ctl( e_fd, EPOLL_CTL_ADD, m_sock, &ev );
		if ( 0 != c_fd ) {
#if DEBUG_MODE
			cerr << "epoll_ctl failed" << endl;
#endif
			close( e_fd );
			close( m_sock );
			m_sock = -1;
			errno = 0;
			free( tmp );
			return false;
		}

#endif // NON_BLOCKING_IO

		int con_ret = ::connect( m_sock, (struct sockaddr *)&m_addr, sizeof( struct sockaddr ) );
		free( tmp );

#if NON_BLOCKING_IO
		if ( con_ret < 0 && errno != EINPROGRESS ) {
#if DEBUG_MODE
			cerr << "failed to connect to " << m_host_name.c_str() << endl;
#endif
			close( e_fd );
			close( m_sock );
			m_sock = -1;
			return false;
		} else if ( 0 != con_ret ) {
			int timeout = m_cutofftime - time( NULL );
			if ( timeout < 0 ) {
#if DEBUG_MODE
				cerr << "timed out for " << m_host_name.c_str() << endl;
#endif
				errno = 0;
				close( e_fd );
				close( m_sock );
				m_sock = -1;
				return false;
			}

			int num_events = epoll_wait( e_fd, events, MAX_EPOLL_EVENTS, timeout * 1000 );
			for ( int i = 0; i < num_events; i++ ) {
				if ( events[i].events & EPOLLERR || events[i].events & EPOLLHUP ) {
#if DEBUG_MODE
					cerr << "epoll error or HUP for " << m_host_name.c_str() << endl;
#endif
					con_ret = -1;
					break;
				} else if ( events[i].events & EPOLLOUT ) {
					con_ret = 0;
					break;
				}
			}
		}

		close( e_fd );
#endif // NON_BLOCKING_IO

		return ( 0 == con_ret );
	}
	catch( exception& ex ) {
		cerr << "exception in HTTP_Checker::connect(): for host '" << m_host_name.c_str() << "'" << endl;
		return false;
	}
}

#endif // USE_GETADDRINFO

bool HTTP_Checker::connect() {
	try {
#if USE_GETADDRINFO
		if ( ! this->connect_getaddrinfo() ) {
#else
		if ( ! this->connect_gethostbyname() ) {
#endif
#if DEBUG_MODE
			int so_error;
			socklen_t len = sizeof so_error;
			::getsockopt( m_sock, SOL_SOCKET, SO_ERROR, &so_error, &len );
			if ( 0 != so_error ) {
				cerr << "socket connect error: " << m_host_name.c_str() << " : " << strerror( so_error ) << endl;
			}
#endif
			if ( -1 != m_sock ) {
				close( m_sock );
				m_sock = -1;
			}
			errno = 0;
			return false;
		}

#if DEBUG_MODE
		cerr << "connected!" << endl;
#endif

		if ( m_is_ssl ) {
			if ( ! this->init_ssl() )
				return false;

			m_sbio = BIO_new_socket( m_sock, BIO_NOCLOSE );
			if ( NULL == m_sbio ) {
#if DEBUG_MODE
				cerr << "The SSL socket alloc failed" << endl;
#endif
				close( m_sock );
				m_sock = -1;
				errno = 0;
				return false;
			}

			SSL_set_bio( m_ssl, m_sbio, m_sbio );
			SSL_set_tlsext_host_name( m_ssl, m_host_name.c_str() );

#if NON_BLOCKING_IO
			int status;
			bool want_read = false;
			bool want_write = false;
			do {
				status = SSL_connect( m_ssl );
				switch ( SSL_get_error( m_ssl, status ) ) {
					case SSL_ERROR_NONE:
						status = 0;
						break;
					case SSL_ERROR_WANT_WRITE:
						want_write = true;
						status = 1;
						break;
					case SSL_ERROR_WANT_READ:
						want_read = true;
						status = 1;
						break;
					case SSL_ERROR_ZERO_RETURN:
						// The peer has notified us that it is shutting down via
						// the SSL "close_notify" message so we need to shutdown, too.
						status = -1;
						break;
					case SSL_ERROR_SYSCALL:
						if ( EWOULDBLOCK == errno && -1 == status ) {
							// Although the SSL_ERROR_WANT_READ/WRITE isn't getting
							// set correctly, the read/write state should be valid.
							errno = 0;
							status = 1;
							if ( SSL_want_write( m_ssl ) ) {
								want_write = true;
							} else if ( SSL_want_read( m_ssl ) ) {
								want_read = true;
							} else {
								status = -1;
							}
						} else {
							status = -1;
						}
						break;
					default:
						status = -1;
						break;
				}

				if ( 1 == status ) {
					if ( ! want_read && ! want_write ) {
#if DEBUG_MODE
						cerr << "The SSL connect failed for " << m_host_name.c_str() << endl;
#endif
						return false;
					}

					fd_set read_fds, write_fds;
					if ( want_read ) {
						FD_ZERO( &read_fds );
						FD_SET( m_sock, &read_fds );
					}
					if ( want_write ) {
						FD_ZERO( &write_fds );
						FD_SET( m_sock, &write_fds );
					}

					struct timeval tv;
					tv.tv_sec = m_cutofftime - time( NULL );
					tv.tv_usec = 0;
					status = ::select( m_sock + 1, &read_fds, &write_fds, NULL, &tv );

					// 0 is timeout, -1 is error, or one or both handles could be set
					if ( status >= 1 ) {
						status = 1;
					} else {
						status = -1;
					}
				}
			} while ( 1 == status && ! SSL_is_init_finished( m_ssl ) && m_cutofftime > time( NULL ) );

			if ( 0 != status || ! SSL_is_init_finished( m_ssl ) ) {
#if DEBUG_MODE
				cerr << "The SSL handshake failed for " << m_host_name.c_str()
					<< ERR_error_string( ERR_get_error(), NULL ) << endl;
#endif
				return false;
			}

#else // NON_BLOCKING_IO
			int status = SSL_connect( m_ssl );

			if ( 1 != status ) {
#if DEBUG_MODE
				cerr << "The SSL handshake failed for " << m_host_name.c_str()
					<< ERR_error_string( ERR_get_error(), NULL ) << endl;
#endif
				return false;
			}
#endif // NON_BLOCKING_IO

			X509* cert = SSL_get_peer_certificate( m_ssl );
			if ( cert ) {
				X509_free( cert );
			}
		}
		return true;
	}
	catch( exception& ex ) {
		cerr << "exception in HTTP_Checker::connect(): for host '" << m_host_name.c_str() << "'" << endl;
		return false;
	}
}

#if NON_BLOCKING_IO
void HTTP_Checker::disconnect_ssl() {
	int status;
	// attempt shutdown for a max of 3 seconds
	time_t waittime = time( NULL ) + 3;
	if ( m_cutofftime < waittime ) {
		waittime = m_cutofftime;
	}
	do {
#if DEBUG_MODE
		cerr << "SSL shutdown handshake for " << m_host_name.c_str() << endl;
#endif
		status = SSL_shutdown( m_ssl );
		switch ( status ) {
			case 1:
#if DEBUG_MODE
				cerr << "clean shutdown : " << m_host_name.c_str() << endl;
#endif
				return;
			case -1:
#if DEBUG_MODE
				cerr << "shutdown failed: " << m_host_name.c_str() << endl;
#endif
				ERR_print_errors_fp( stderr );
				return;
			default:
#if DEBUG_MODE
				cerr << "shutdown not yet finished : " << m_host_name.c_str() << endl;
#endif
				break;
		}
		switch ( SSL_get_error( m_ssl, status ) ) {
			case SSL_ERROR_WANT_WRITE:
			case SSL_ERROR_WANT_READ:
#if DEBUG_MODE
				cerr << "want read/write : " << m_host_name.c_str() << endl;
#endif
				fd_set read_fds, write_fds;
				FD_ZERO( &read_fds );
				FD_ZERO( &write_fds );
				FD_SET( m_sock, &read_fds );
				FD_SET( m_sock, &write_fds );

				struct timeval tv;
				tv.tv_sec = waittime - time( NULL );
				tv.tv_usec = 0;
#if DEBUG_MODE
				cerr << "selecting : " << m_host_name.c_str() << endl;
#endif
				status = ::select( m_sock + 1, &read_fds, &write_fds, NULL, &tv );
#if DEBUG_MODE
				cerr << "select result : " << status << endl;
#endif
				if ( status >= 1 ) {
					status = 1;
				}
				break;
			case SSL_ERROR_SYSCALL:
				// From the man page:
				// The output of SSL_get_error(3) may be misleading, as an erroneous
				// SSL_ERROR_SYSCALL may be flagged even though no error occurred.
				status = 1;
				break;
			default:
#if DEBUG_MODE
				cerr << "generic error for : " << m_host_name.c_str() << endl;
#endif
				status = 1;
				break;
		}
	} while ( 1 == status && waittime > time( NULL ) );
}
#endif // NON_BLOCKING_IO

bool HTTP_Checker::disconnect() {
	try {
		if ( m_is_ssl ) {
			if ( NULL != m_ssl ) {
#if NON_BLOCKING_IO
				this->disconnect_ssl();
#else
				// attempt shutdown for a max of 3 seconds
				time_t waittime = time( NULL ) + 3;
				if ( m_cutofftime < waittime ) {
					waittime = m_cutofftime;
				}
				int res = SSL_shutdown( m_ssl );
				while ( 1 != res && waittime > time( NULL ) ) {
					res = SSL_shutdown( m_ssl );
					sleep( 1 );
				}
#if DEBUG_MODE
				if ( 1 == res ) {
					cerr << "client exited gracefully: " << m_host_name.c_str() << endl;
				} else {
					cerr << "error in shutdown for " <<  m_host_name.c_str() << endl;
					ERR_print_errors_fp( stderr );
				}
#endif // DEBUG_MODE
#endif // NON_BLOCKING_IO
				SSL_free( m_ssl );
				m_ssl = NULL;
			}
			if ( NULL != m_ctx ) {
				SSL_CTX_free( m_ctx );
				m_ctx = NULL;
			}
		}
		if ( m_sock > 0 ) {
			if ( ::shutdown( m_sock, SHUT_RDWR ) != 0 ) {
				errno = 0;
			}
			::close( m_sock );
			m_sock = -1;
		}
		return true;
	}
	catch( exception &ex ) {
		cerr << "exception in HTTP_Checker::disconnect(): for host '" << m_host_name.c_str() << "'" << endl;
		return false;
	}
}

bool HTTP_Checker::send_bytes( char* p_packet, size_t p_packet_length ) {
	try {
		ssize_t bytes_left = p_packet_length;
		ssize_t bytes_sent = 0;
		int send_attempts = 5;
		ssize_t bytes_to_send = 0;

		do
		{
			if ( bytes_left < MAX_TCP_BUFFER )
				bytes_to_send = bytes_left;
			else
				bytes_to_send = MAX_TCP_BUFFER;

			if ( m_is_ssl )
				bytes_sent = SSL_write( m_ssl, (const char *)p_packet, (int)bytes_to_send );
			else
				bytes_sent = ::send( this->m_sock, (const char *)p_packet, (int)bytes_to_send, 0 );

			if ( bytes_sent != bytes_to_send ) {
				switch ( errno ) {
					case ENOTSOCK: {
#if DEBUG_MODE
						cerr << "ERROR: socket operation on non-socket irrecoverable; aborting" << endl;
#endif
						return false;
					}
					case EBADF: {
#if DEBUG_MODE
						cerr << "ERROR: bad file descriptor is irrecoverable; aborting" << endl;
#endif
						return false;
					}
					case EPIPE: {
#if DEBUG_MODE
						cerr << "ERROR: broken pipe is irrecoverable; aborting" << endl;
#endif
						return false;
					}
					default: {
#if DEBUG_MODE
						cerr << "ERROR: unknown error (" << errno << "); aborting" << endl;
#endif
						return false;
					}
				}
			}
			if ( bytes_sent > 0 ) {
				bytes_left -= bytes_sent;
				p_packet += bytes_sent;
			}
		} while( ( bytes_left > 0 ) && --send_attempts && m_cutofftime > time( NULL ) );

		return ( bytes_left == 0 );
	}
	catch( exception & ex ) {
		cerr << "exception in HTTP_Checker::send_bytes(): for host '" << m_host_name.c_str() << "'" << endl;
		return false;
	}
}

