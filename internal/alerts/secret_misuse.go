package alerts

// SecretMisuse reports a trusted egress rejection involving a private secret
// or decoy. Public details contain only a validated name and destination.
const SecretMisuse="secret_misuse"

func init(){
 Kinds=append(Kinds,SecretMisuse)
 DefaultEvents=append(DefaultEvents,SecretMisuse)
 StreamKinds=append(StreamKinds,SecretMisuse)
}
