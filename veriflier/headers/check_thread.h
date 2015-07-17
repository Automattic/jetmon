
#ifndef __CHECKTHREAD_H__
#define __CHECKTHREAD_H__

#include <QThread>
#include <QtNetwork/QAbstractSocket>
#include <QtNetwork/QSslSocket>
#include <QJsonValue>
#include <QDateTime>
#include <QString>
#include <QHostAddress>
#include <QUrl>

#include "headers/config.h"
#include "headers/http_checker.h"

#define HOST_DOWN           0
#define HOST_ONLINE         1

class CheckThread : public QThread
{
	Q_OBJECT
public:
	CheckThread( const QSslConfiguration *m_ssl_config,
				const int net_timeout,
				const bool debug );

	void setMonitorUrl( QString monitor_url ) { m_monitor_url = monitor_url; }
	void setBlogID( qint64 blog_id ) { m_blog_id = blog_id; }
	void setTimer( const QDateTime &requested ) { m_timer = requested; }

protected:
	void run() Q_DECL_OVERRIDE;

signals:
	void resultReady( qint64 blog_id, int status );

private:
	QSslSocket *m_socket;
	const QSslConfiguration *m_ssl_config;

	QString m_monitor_url;
	int m_blog_id;
	int m_status;
	int m_net_timeout;

	QDateTime m_timer;
	bool m_debug;

	void performHostCheck();
};

#endif // __CHECKTHREAD_H__
