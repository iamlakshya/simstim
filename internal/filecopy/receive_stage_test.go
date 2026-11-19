package filecopy

import (
 "bytes"
 "context"
 "crypto/sha256"
 "encoding/hex"
 "errors"
 "os"
 "path/filepath"
 "testing"
)

func TestPreparedCopyStageRequiresExactBytesAndDigestBeforePublication(t *testing.T){
 data:=[]byte("complete checked bytes");sum:=sha256.Sum256(data);digest:=hex.EncodeToString(sum[:])
 for _,kind:=range []string{"exact","short","extra","changed","cancelled","nonempty"}{t.Run(kind,func(t *testing.T){
  file,err:=os.CreateTemp(t.TempDir(),"private-stage-");if err!=nil{t.Fatal(err)};defer file.Close()
  content:=append([]byte(nil),data...);ctx:=context.Background()
  switch kind{
  case "short":content=content[:len(content)-1]
  case "extra":content=append(content,'x')
  case "changed":content[0]^=1
  case "cancelled":cancelled,cancel:=context.WithCancel(ctx);cancel();ctx=cancelled
  case "nonempty":if _,err:=file.Write([]byte("foreign"));err!=nil{t.Fatal(err)}
  }
  err=ReceivePreparedStage(ctx,file,int64(len(data)),digest,bytes.NewReader(content))
  if (kind=="exact")!=(err==nil){t.Fatal("prepared stage result differs",kind,err)}
  if kind=="exact"{
   got,err:=os.ReadFile(file.Name());if err!=nil || !bytes.Equal(got,data){t.Fatal("prepared descriptor bytes differ",err)}
   info,err:=file.Stat();if err!=nil || info.Mode().Perm()!=0644{t.Fatal("prepared descriptor mode differs",err)}
  }
  // No variant owns a destination name, including success. Only the
  // retained original factory's separate publisher may create one.
  if _,err:=os.Lstat(filepath.Join(filepath.Dir(file.Name()),"destination"));!errors.Is(err,os.ErrNotExist){t.Fatal("data validation published a caller path")}
 })}
}
