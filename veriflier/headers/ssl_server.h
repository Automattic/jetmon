
#ifndef __SSL_SERVER_H__
#define __SSL_SERVER_H__

#include <QTcpServer>
#include <QThreadPool>

#include "headers/config.h"
#include "headers/client_thread.h"

class SSL_Server : public QTcpServer
{
	Q_OBJECT
public:
	SSL_Server( QObject *parent = 0 );
	~SSL_Server();

protected:
	void incomingConnection( qintptr socketDescriptor );

private:
	QDateTime ticker;
	QString m_veriflier_name;
	QString m_auth_token;
	int m_net_comms_timeout;
	int m_served_count;
	bool m_debug;
};

#endif // __SSL_SERVER_H__

