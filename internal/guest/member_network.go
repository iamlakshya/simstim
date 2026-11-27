package guest

import "github.com/iamlakshya/simstim/internal/proto"

func (g *Init) memberProxyEnv(a *proto.CtlStartArgs, primary bool) error {
	if g.vm != nil {
		return g.vm.memberProxyEnv(a, primary)
	}
	if primary && a.Proxy != "" {
		proxy, err := StartProxy(g.sess, a.Proxy, g.log)
		if err != nil {
			return err
		}
		g.mu.Lock()
		if g.ended {
			g.mu.Unlock()
			proxy.Close()
			return execError("the guest stopped before its proxy started")
		}
		g.proxy = proxy
		g.mu.Unlock()
	}
	g.mu.Lock()
	proxy := g.proxy
	g.mu.Unlock()
	if proxy != nil {
		a.Env = rewriteProxyEnv(a.Env, proxy.URL())
	}
	return nil
}
