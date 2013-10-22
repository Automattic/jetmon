
#include "http_checker.h"
#include <stdlib.h>

using namespace std;

HTTP_Checker::HTTP_Checker() : m_sock( -1 ), m_host_name( "" ), m_str_desc( "" ), m_port( 0 ), m_triptime( 0 ) {
	;
}

HTTP_Checker::~HTTP_Checker() {
	this->disconnect();
}

void HTTP_Checker::check( string p_host_name, int p_port ) {
	struct timeval m_tstart;
	struct timeval m_tend;

	m_host_name = p_host_name;
	m_port = p_port;

	if ( init_socket() && connect() ) {
		gettimeofday( &m_tstart, &m_tzone );
        string response = send_http_get();
        if ( response.size() > 0 ) {
        	gettimeofday( &m_tend, &m_tzone );
			if ( (m_tend.tv_usec -= m_tstart.tv_usec) < 0 )   {
				m_tend.tv_sec--;
				m_tend.tv_usec += 1000000;
			}
			m_tend.tv_sec -= m_tstart.tv_sec;
			m_triptime = m_tend.tv_sec * 1000000 + ( m_tend.tv_usec );
			if ( response.find_first_of( ' ' ) == 8 ) {
				string code = response.substr( 9, 3 );
                if ( response.find( "Jetpack:" ) != std::string::npos && 400 > atoi( code.c_str() ) ) {
                    code = string( "SITE OK" );
                }
				m_str_desc = code;					// this will be the status code, 200, 301, 302, 403, 404, etc.
			} else {
				m_str_desc = "Status code unknown";
			}
		} else {
			m_str_desc = "no response - timed out";
		}
	}
}

string HTTP_Checker::send_http_get() {
	/*
	time_t current;
    char rfc_2822[40];
    time( &current );
    strftime( rfc_2822, sizeof( rfc_2822 ), "%a, %d %b %Y %T %z", localtime( &current ) );
    */
	string s_tmp = "HEAD / HTTP/1.1\r\nHost: " + m_host_name + "\r\nuser-agent: jetmon\r\nConnection: Close\r\n\r\n";

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
	size_t received;
    fd_set read_fds;
    struct timeval tv;
    time_t time_end = time(NULL);
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
		received = ::recv(m_sock, m_buf, MAX_TCP_BUFFER, 0);

		while ( received > 0 ) {
			m_buf[received] = '\0';
            ret_val += m_buf;
			time_end = time(NULL);
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
				received = ::recv(m_sock, (char*)m_buf, MAX_TCP_BUFFER, 0);
			else
				received = 0;
		}
	}
    return ret_val;
}

bool HTTP_Checker::init_socket() {
    m_sock = ::socket( AF_INET, SOCK_STREAM, IPPROTO_TCP );

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

    long flags = fcntl( m_sock, F_GETFL );
    ret_val = fcntl( m_sock, F_SETFL, flags | O_NONBLOCK );

    if ( -1 == ret_val ) {
        close( m_sock );
        m_sock = -1;
        errno = 0;
        m_str_desc = "unable to set socket option O_NONBLOCK";
        return false;
    } else {
		return true;
	}
}

bool HTTP_Checker::connect() {
	try {
		struct sockaddr_in m_addr;

		struct hostent *hp = gethostbyname( m_host_name.c_str() );
		if ( hp ) {
			m_addr.sin_port = htons( m_port );
			m_addr.sin_family = hp->h_addrtype;
			bcopy( hp->h_addr, (caddr_t)&m_addr.sin_addr, hp->h_length );
			m_host_name = hp->h_name;
		} else {
			m_str_desc = "unknown host:" + m_host_name;
			return false;
		}

		int ret_val = ::connect( m_sock, (struct sockaddr *)&m_addr, sizeof( struct sockaddr ) );

        fd_set write_fds;
        struct timeval tv;

        FD_ZERO( &write_fds );
        FD_SET( m_sock, &write_fds );

        tv.tv_sec = NET_COMMS_TIMEOUT;
        tv.tv_usec = 0;

        ret_val = select(m_sock + 1, NULL, &write_fds, NULL, &tv);

        switch ( ret_val ) {
            case 0:
                m_str_desc = "connect timeout";
                return false;
            case -1:
                m_str_desc = "error performing connect";
                return false;
            default:
                int so_error;
                socklen_t len = sizeof so_error;

                ::getsockopt(m_sock, SOL_SOCKET, SO_ERROR, &so_error, &len);

                if ( 0 != so_error ) {
                    m_str_desc = "socket connect error: ";
                    m_str_desc += strerror( so_error );
                    close( m_sock );
                    m_sock = -1;
                    errno = 0;
                    return false;
                }
                break;
        }
        return true;
	}
	catch( exception& ex ) {
		m_str_desc = "exception in HTTP_Checker::connect()";
		return false;
	}
}

bool HTTP_Checker::disconnect() {
	try
	{
		if(m_sock > 0) {
			if ( ::shutdown( m_sock, SHUT_RDWR ) != 0 )
				errno = 0;

			::close(m_sock);
			m_sock = -1;
		}
		return true;
	}
	catch( exception &ex ) {
		return false;
	}
}

bool HTTP_Checker::send_bytes( char* p_packet, size_t p_packet_length )
{
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

		bytes_sent = ::send(this->m_sock, (const char *)p_packet, (int)bytes_to_send, 0);

		if ( bytes_sent != bytes_to_send ) {
			//cerr << "data send failed (sent " << bytes_sent <<  ") retries left " << send_attempts << ") - (" << errno <<  ") " << endl;

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


