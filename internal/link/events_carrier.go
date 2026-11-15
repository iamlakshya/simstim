package link

import ("bufio";"net")

// ContinueEvents owns the same buffered connection after its checked ordinary
// hello and successful events response. Only the host opens one events stream;
// the ordinary guest-init opener table is unchanged. Records are JSON lines.
func ContinueEvents(conn net.Conn,reader *bufio.Reader,side Side)*Session {
 s:=newSession(conn,side,Options{Window:DefaultWindow,MaxSupervisorStreams:1,MaxGuestStreams:1,Opener:map[Kind]Side{KindEvents:Supervisor}})
 s.helloOK=true;close(s.hello);s.wg.Add(2)
 go func(){defer s.wg.Done();s.fail(s.readContinuation(reader))}()
 go func(){defer s.wg.Done();s.writeContinuation()}()
 return s
}
