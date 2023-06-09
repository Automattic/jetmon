
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
	CheckThread( const int net_timeout, const bool debug,
				const int thread_index );

	void performCheck( HealthCheck* hc );

protected:
	void run();

signals:
	void resultReady( int thread_index, qint64 blog_id, QString monitor_url, int status, int http_code, int rtt );

public slots:
	void finishedCheck( HTTP_Checker *checker, HealthCheck* hc );

private:
	QVector<HealthCheck*> m_checkers;

	int m_net_timeout;
	int m_thread_index;

	QDateTime m_timer;
	bool m_debug;

	void performHostCheck();
};

#endif // __CHECKTHREAD_H__
