
#include "http_checker.h"

using namespace std;

HTTP_Checker::HTTP_Checker() : m_sock( -1 ), m_host_name( "" ), m_str_desc( "" ), m_host_dir( "" ),
								m_port( HTTP_DEFAULT_PORT ), m_triptime( 0 ), m_response_code( 0 ),
								m_ctx( NULL ), m_ssl( NULL ), m_sbio( NULL ) {
	memset( m_buf, 0, MAX_TCP_BUFFER );
}

HTTP_Checker::~HTTP_Checker() {
	this->disconnect();
}

void HTTP_Checker::check( string p_host_name, int p_port ) {
	try {
		m_host_name = p_host_name;
		m_port = p_port;
		m_host_dir = '/';

		this->parse_host_values();

		if ( connect() )
			this->set_host_response( 0 );
	}
	catch( exception &ex ) {
		m_str_desc = "exception in HTTP_Checker::check(): for host '" + p_host_name + "'";
		cerr << "exception in HTTP_Checker::check(): for host '" << p_host_name.c_str() << "'" << std::endl;
	}
}

void HTTP_Checker::set_host_response( int redirects ) {
	try {
		struct timeval m_tstart;
		struct timeval m_tend;

		gettimeofday( &m_tstart, &m_tzone );
		string response = send_http_get();
		if ( response.size() > 0 ) {
			gettimeofday( &m_tend, &m_tzone );
			if ( (m_tend.tv_usec -= m_tstart.tv_usec) < 0 ) {
				m_tend.tv_sec--;
				m_tend.tv_usec += 1000000;
			}
			m_tend.tv_sec -= m_tstart.tv_sec;
			m_triptime = m_tend.tv_sec * 1000000 + ( m_tend.tv_usec );
			if ( response.find_first_of( ' ' ) == 8 ) {
				m_str_desc = response.substr( 9, 3 );
				m_response_code = atoi( m_str_desc.c_str() );

				// if we have been redirected, get the details and make a recursive call
				if ( ( 300 < m_response_code ) && ( 400 > m_response_code ) ) {
					if ( set_redirect_host_values( response ) ) {
						this->disconnect();
						if ( connect() ) {
							redirects++;
							if ( MAX_REDIRECTS >= redirects )
								this->set_host_response( redirects );
							else
								m_str_desc = "Hit max on the redirects";
						}
					} else {
						m_str_desc = "Unable to determine redirect host";
						m_response_code = 404;
					}
				}
			} else {
				m_str_desc = "Status code unknown";
				m_response_code = 404;
			}
		} else {
			m_str_desc = "no response - timed out";
		}
	}
	catch( exception &ex ) {
		m_str_desc = "exception in HTTP_Checker::set_host_responses(): for host '" + m_host_name + "'";
		cerr << "exception in HTTP_Checker::set_host_responses(): for host '" << m_host_name.c_str() << "'" << std::endl;
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

		this->parse_host_values();

		// this is a relative location redirect, reinstate hostname
		if ( 0 == m_host_name.size() )
			m_host_name = hostname_backup;

		return true;
	}
	catch( exception &ex ) {
		m_str_desc = "exception in HTTP_Checker::set_redirect_host_values()";
		cerr << "exception in HTTP_Checker::set_redirect_host_values()" << std::endl;
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
	}

	if ( string::npos != m_host_name.find_first_of( ':' ) ) {
		m_port = atoi( m_host_name.substr( m_host_name.find_first_of( ':' ) + 1, min( m_host_name.find_first_of( '/' ), m_host_name.length() ) ).c_str() );
		m_host_name.erase( m_host_name.find_first_of( ':' ), min( m_host_name.find_first_of( '/' ), m_host_name.length() ) - m_host_name.find_first_of( ':' ) );
	}

	if ( string::npos != m_host_name.find_first_of( '/' ) ) {
		m_host_dir = m_host_name.substr( m_host_name.find_first_of( '/' ), m_host_name.length() - m_host_name.find_first_of( '/' ) );
		m_host_name.erase( m_host_name.find_first_of( '/' ), m_host_name.length() - m_host_name.find_first_of( '/' ) );
	}
}

string HTTP_Checker::send_http_get() {
	string s_tmp = "HEAD " + m_host_dir + " HTTP/1.1\r\n";
			s_tmp += "Host: " + m_host_name + "\r\n";
			s_tmp += "User-Agent: jetmon/1.0 (Jetpack Site Uptime Monitor by WordPress.com)\r\n";
			s_tmp += "Connection: Close\r\n\r\n";

	strcpy( m_buf, s_tmp.c_str() );

	if ( send_bytes( m_buf, s_tmp.length() ) ) {
		s_tmp = get_response();
	} else {
		s_tmp = "";
		m_str_desc = "failed to send_bytes()";
	}
	return s_tmp;
}

std::string HTTP_Checker::get_response() {
	try {
		size_t received;
		fd_set read_fds;
		struct timeval tv;
		time_t time_end = time( NULL );
		time_end += NET_COMMS_TIMEOUT;
		string ret_val = "";

		do {
			tv.tv_sec = 0;
			tv.tv_usec = 50000;

			FD_ZERO( &read_fds );
			FD_SET( m_sock, &read_fds );

			::select( m_sock + 1, &read_fds, NULL, NULL, &tv );
		}while ( (FD_ISSET( m_sock, &read_fds ) == 0) && ( time_end > time( NULL ) ) );

		if ( FD_ISSET( m_sock, &read_fds) ) {
			if ( HTTPS_DEFAULT_PORT == m_port )
				received = SSL_read( m_ssl, m_buf, MAX_TCP_BUFFER - 1 );
			else
				received = ::recv( m_sock, m_buf, MAX_TCP_BUFFER - 1, 0 );

			while ( received > 0 ) {
				if ( received < MAX_TCP_BUFFER )
					m_buf[ received ] = '\0';
				if ( ( (size_t)-1 ) != received )
					ret_val += m_buf;

				time_end = time( NULL );
				time_end += NET_COMMS_TIMEOUT;

				do
				{
					tv.tv_sec = 0;
					tv.tv_usec = 50000;

					FD_ZERO( &read_fds );
					FD_SET( m_sock, &read_fds );

					select( m_sock + 1, &read_fds, NULL, NULL, &tv );
				}while( (FD_ISSET( m_sock, &read_fds ) == 0) && ( time_end > time( NULL ) ) );

				if( FD_ISSET( m_sock, &read_fds) )
					if ( HTTPS_DEFAULT_PORT == m_port )
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
		m_str_desc = "exception in HTTP_Checker::get_response(): for host '" + m_host_name + "'";
		cerr << "exception in HTTP_Checker::get_response(): for host '" << m_host_name.c_str() << "'" << std::endl;
		return "";
	}
}

bool HTTP_Checker::init_socket( addrinfo *addr ) {
	m_sock = ::socket( addr->ai_family, addr->ai_socktype, addr->ai_protocol );

	if ( -1 == m_sock ) {
		errno = 0;
		m_str_desc = "unable to create socket";
		return false;
	}

	int val = 1;
	int ret_val = ::setsockopt( m_sock, SOL_SOCKET, SO_REUSEADDR, &val, sizeof( val ) );

	if( -1 == ret_val ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
		m_str_desc = "unable to set socket option SO_REUSEADDR";
		return false;
	}

	struct timeval time_out;
	time_out.tv_sec = NET_COMMS_TIMEOUT;
	time_out.tv_usec = 0;

	ret_val = ::setsockopt( m_sock, SOL_SOCKET, SO_SNDTIMEO, &time_out, sizeof( time_out ) );

	if( -1 == ret_val ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
		m_str_desc = "unable to set socket option SO_SNDTIMEO";
		return false;
	}

	ret_val = ::setsockopt( m_sock, SOL_SOCKET, SO_RCVTIMEO, &time_out, sizeof( time_out ) );

	if( -1 == ret_val ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
		m_str_desc = "unable to set socket option SO_RCVTIMEO";
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
		m_str_desc = "unable to set SSL context";
		cerr << "unable to set SSL context" << endl;
		return false;
	}

	m_ssl = SSL_new( m_ctx );

	if ( NULL == m_ssl ) {
		close( m_sock );
		m_sock = -1;
		errno = 0;
		m_str_desc = "unable to set init SSL";
		cerr << "unable to set init SSL" << endl;
		return false;
	}

	SSL_set_mode( m_ssl, SSL_MODE_AUTO_RETRY );
	return true;
}

bool HTTP_Checker::connect() {
	try {
        addrinfo *res = 0;
		struct addrinfo hints;
		memset( &hints, 0, sizeof( hints ) );
		hints.ai_family = AF_UNSPEC;
		hints.ai_flags = AI_ADDRCONFIG;
		hints.ai_socktype = SOCK_STREAM;
		int con_ret = -1;
		int result = -1;

		if ( HTTPS_DEFAULT_PORT == m_port )
			result = getaddrinfo( m_host_name.c_str(), "https", &hints, &res );
		else
			result = getaddrinfo( m_host_name.c_str(), "http", &hints, &res );

		if ( EAI_BADFLAGS == result ) {
			hints.ai_flags = 0;
			if ( HTTPS_DEFAULT_PORT == m_port )
				result = getaddrinfo( m_host_name.c_str(), "https", &hints, &res );
			else
				result = getaddrinfo( m_host_name.c_str(), "http", &hints, &res );
		}

		if ( 0 == result ) {
			addrinfo *node = res;
			while ( node ) {
				if ( ( AF_INET == node->ai_family ) || ( AF_INET6 == node->ai_family ) ) {
					if ( init_socket( node ) )
						con_ret = ::connect( m_sock, node->ai_addr, node->ai_addrlen );

					if ( con_ret == 0 ) {
						break;
					} else {
						close( m_sock );
						m_sock = -1;
					}
				}
				node = node->ai_next;
			}
			freeaddrinfo( res );
		}/* else if ( EAI_NONAME == result || EAI_FAIL == result ) {
			std::cerr << "host not found: " << m_host_name.c_str() << std::endl;
		}*/

		if ( 0 != con_ret ) {
			if ( -1 != m_sock ) {
				close( m_sock );
				m_sock = -1;
			}
			errno = 0;
			return false;
		}

		if ( HTTPS_DEFAULT_PORT == m_port ) {
			if ( ! this->init_ssl() )
				return false;

			m_sbio = BIO_new_socket( m_sock, BIO_NOCLOSE );

			if ( NULL == m_sbio ) {
				m_str_desc = "The SSL socket alloc failed";
				cerr << "The SSL socket alloc failed" << endl;
				close( m_sock );
				m_sock = -1;
				errno = 0;
				return false;
			}

			SSL_set_bio( m_ssl, m_sbio, m_sbio );
			int ssl_val = SSL_connect( m_ssl );

			if ( 1 != ssl_val ) {
				m_str_desc = "The SSL handshake failed: " + m_host_name;
				//cerr << "The SSL handshake failed: " << m_host_name.c_str() << endl;
				return false;
			}
		}
		return true;
	}
	catch( exception& ex ) {
		m_str_desc = "exception in HTTP_Checker::connect(): for host '" + m_host_name + "'";
		cerr << "exception in HTTP_Checker::connect(): for host '" << m_host_name.c_str() << "'" << std::endl;
		return false;
	}
}

bool HTTP_Checker::disconnect() {
	try {
		if ( HTTPS_DEFAULT_PORT == m_port ) {
			if ( NULL != m_ssl ) {
				int ret_val = SSL_shutdown( m_ssl );
				if ( 0 == ret_val )
					ret_val = SSL_shutdown( m_ssl );
				if ( -1 == ret_val )
					ERR_print_errors_fp( stderr );
			}
			if ( NULL != m_ctx )
				SSL_CTX_free( m_ctx );
			if ( NULL != m_ssl )
				SSL_free( m_ssl );
			m_ctx = NULL;
			m_ssl = NULL;
		}
		if ( m_sock > 0 ) {
			if ( ::shutdown( m_sock, SHUT_RDWR ) != 0 )
				errno = 0;

			::close( m_sock );
			m_sock = -1;
		}
		return true;
	}
	catch( exception &ex ) {
		m_str_desc = "exception in HTTP_Checker::disconnect(): for host '" + m_host_name + "'";
		cerr << "exception in HTTP_Checker::disconnect(): for host '" << m_host_name.c_str() << "'" << std::endl;
		return false;
	}
}

bool HTTP_Checker::send_bytes( char* p_packet, size_t p_packet_length ) {
	try {
		size_t bytes_left = p_packet_length;
		size_t bytes_sent = 0;
		int send_attempts = 5;
		size_t bytes_to_send = 0;

		do
		{
			if ( bytes_left < MAX_TCP_BUFFER )
				bytes_to_send = bytes_left;
			else
				bytes_to_send = MAX_TCP_BUFFER;

			if ( HTTPS_DEFAULT_PORT == m_port )
				bytes_sent = SSL_write( m_ssl, (const char *)p_packet, (int)bytes_to_send );
			else
				bytes_sent = ::send( this->m_sock, (const char *)p_packet, (int)bytes_to_send, 0 );

			if ( bytes_sent != bytes_to_send ) {
				switch ( errno ) {
					case ENOTSOCK: {
						m_str_desc = "ERROR: socket operation on non-socket irrecoverable; aborting";
						return false;
						break;
					}
					case EBADF: {
						m_str_desc = "ERROR: bad file descriptor is irrecoverable; aborting";
						return false;
						break;
					}
					case EPIPE: {
						m_str_desc = "ERROR: broken pipe is irrecoverable; aborting";
						return false;
					}
					default: {
						m_str_desc = "ERROR: unknown error; aborting";
						cerr << "ERROR: unknown error (" << errno << "); aborting" << endl;
						return false;
					}
				}

				errno = 0;
				usleep( 1000 );
			}
			if ( bytes_sent != (unsigned)-1 ) {
				bytes_left -= bytes_sent;
				p_packet += bytes_sent;
			}
		} while( (bytes_left > 0 ) && --send_attempts );

		return ( bytes_left == 0 );
	}
	catch( exception & ex ) {
		m_str_desc = "exception in HTTP_Checker::send_bytes(): for host '" + m_host_name + "'";
		cerr << "exception in HTTP_Checker::send_bytes(): for host '" << m_host_name.c_str() << "'" << std::endl;
		return false;
	}
}

