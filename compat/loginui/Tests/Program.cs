// SPDX-License-Identifier: GPL-3.0-or-later
using System.Buffers.Binary;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using Oddward.LoginUi;

internal static class Tests
{
 private static readonly List<string> results = [];
 private static int failures;
 [STAThread] private static int Main(string[] args)
 {
  if(args.Length==1&&args[0]=="--game-fixture")return OverlayTests.FixtureMain();
  if(args.Length==1&&args[0]=="--motion-fixture")return MotionTests.FixtureMain();
  string output = args[0]; Directory.CreateDirectory(output);
  _ = new Application { ShutdownMode = ShutdownMode.OnExplicitShutdown };
  if(args.Length==3&&args[2]=="--motion-only"){MotionTests.Run(args[1],output,Check);return Complete(output);}
  if(args.Length==3&&args[2]=="--child-only"){ChildTests.Run(args[1],output,Check);return Complete(output);}
  Check("bounded framing and malformed input", () => {
   using MemoryStream s = new();
   Protocol.Write(s,new Envelope(Type:"event",Event:new UiEvent("login_phone",Phone:"13800000000",Captcha:"123456")));
   s.Position=0; Require(Protocol.Read(s)?.Event?.Captcha=="123456","round trip");
   foreach(int size in new[]{0,65537,int.MaxValue}) {
    byte[] h = new byte[4];BinaryPrimitives.WriteInt32LittleEndian(h,size);bool rejected=false;
    try{Protocol.Read(new MemoryStream(h));}catch(InvalidDataException){rejected=true;}
    Require(rejected,"size rejected");
   }
   string json="{\"version\":1,\"type\":\"model\",\"model\":{\"screen\":\"phone\",\"token\":\"synthetic-secret\"}}";
   byte[] b=Encoding.UTF8.GetBytes(json);using MemoryStream malformed=new();
   malformed.Write(BitConverter.GetBytes(b.Length));malformed.Write(b);malformed.Position=0;
   bool invalid=false;try{Protocol.Read(malformed);}catch(InvalidDataException e){invalid=!e.Message.Contains("synthetic-secret");}
   Require(invalid,"unknown fields rejected without payload echo");
  });
  Check("overlay binding event preserves canonical handles and rejects cross-kind fields", () => {
   Envelope Decode(string json){byte[] bytes=Encoding.UTF8.GetBytes(json);using MemoryStream wire=new();wire.Write(BitConverter.GetBytes(bytes.Length));wire.Write(bytes);wire.Position=0;return Protocol.Read(wire)!;}
   string Frame(string fields)=>"{\"version\":1,\"type\":\"event\",\"event\":{"+fields+"}}";
   Envelope binding=Decode(Frame("\"kind\":\"bind_overlay\",\"window\":\"123\",\"gameWindow\":\"18446744073709551615\",\"requestId\":\"1\""));
   using MemoryStream encoded=new();Protocol.Write(encoded,binding);string roundTrip=Encoding.UTF8.GetString(encoded.ToArray()[4..]);
   Require(roundTrip.Contains("\"window\":\"123\"")&&roundTrip.Contains("\"gameWindow\":\"18446744073709551615\""),"binding keeps exact unsigned handles");
   string[] invalid=["", "0", "01", "+1", " 1", "1 ", "-1", "18446744073709551616"];
   foreach(string value in invalid)foreach(string field in new[]{"window","gameWindow"}) {
    string fields="\"kind\":\"bind_overlay\",\"window\":\"123\",\"gameWindow\":\"456\",\"requestId\":\"1\"";
    fields=fields.Replace(field=="window"?"\"window\":\"123\"":"\"gameWindow\":\"456\"","\""+field+"\":\""+value+"\"");
    bool rejected=false;try{Decode(Frame(fields));}catch(InvalidDataException){rejected=true;}Require(rejected,"binding rejects noncanonical handles");
   }
   foreach(string fields in new[]{"\"kind\":\"bind_overlay\",\"window\":\"123\"","\"kind\":\"bind_overlay\",\"window\":null,\"gameWindow\":\"456\"","\"kind\":\"bind_overlay\",\"window\":\"123\",\"gameWindow\":\"456\",\"phone\":\"13800000000\"","\"kind\":\"ready\",\"window\":\"123\",\"gameWindow\":\"456\"","\"kind\":\"ready\",\"window\":\"\"","\"kind\":\"ready\",\"gameWindow\":null","\"kind\":\"ready\",\"requestId\":null","\"kind\":\"ready\",\"requestId\":\"\""}) {
    bool rejected=false;try{Decode(Frame(fields));}catch(InvalidDataException){rejected=true;}Require(rejected,"binding fields only appear on complete binding events");
   }
   using MemoryStream ready=new();Protocol.Write(ready,new Envelope(Type:"event",Event:new UiEvent("ready")));
   string plain=Encoding.UTF8.GetString(ready.ToArray()[4..]);Require(!plain.Contains("\"window\"")&&!plain.Contains("\"gameWindow\""),"other events omit HWND fields");
  });
  Check("game process identity survives the model protocol", () => {
   const string json="{\"version\":1,\"type\":\"model\",\"model\":{\"screen\":\"phone\",\"gameProcessId\":42,\"gameCreationTime\":\"134000000000000001\"}}";
   using MemoryStream wire=new();byte[] bytes=Encoding.UTF8.GetBytes(json);
   wire.Write(BitConverter.GetBytes(bytes.Length));wire.Write(bytes);wire.Position=0;
   Envelope decoded=Protocol.Read(wire)!;using MemoryStream encoded=new();Protocol.Write(encoded,decoded);
   string roundTrip=Encoding.UTF8.GetString(encoded.ToArray()[4..]);
   Require(roundTrip.Contains("\"gameProcessId\":42")&&roundTrip.Contains("\"gameCreationTime\":\"134000000000000001\""),"exact PID and FILETIME retained");
  });
  Check("game identity rejects partial noncanonical and overflow values", () => {
   foreach((uint pid,string? time) in new (uint,string?)[]{(42,""),(0,"134000000000000001"),(42,"0"),(42,"01"),(42,"+1"),(42," 1"),(42,"1 "),(42,"-1"),(42,"18446744073709551616"),(42,null)}) {
    bool rejected=false;try{using MemoryStream wire=new();Protocol.Write(wire,new Envelope(Type:"model",Model:Phone() with{GameProcessId=pid,GameCreationTime=time!}));}catch(InvalidDataException){rejected=true;}
    Require(rejected,"reject malformed identity");
   }
   foreach(Model model in new[]{Phone(),Phone() with{GameProcessId=42,GameCreationTime="18446744073709551615"}}){using MemoryStream wire=new();Protocol.Write(wire,new Envelope(Type:"model",Model:model));}
  });
  Check("notice topics and acknowledgment are backend driven", () => {
   List<UiEvent> sent=[]; LoginWindow w=new(sent.Add);Model notice=Notice();w.Apply(notice);Layout(w);
   Require(Descendants((DependencyObject)w.Content).OfType<TextBlock>().Count(x=>x.Text.StartsWith("主题"))==8,"all eight notice topics");
   Require(Find<Button>(w,"official").Visibility==Visibility.Collapsed,"official unavailable hidden");
   Click(w,"primary");Click(w,"primary");Require(sent.Count==1&&sent[0].Kind=="acknowledge","one pending acknowledge");
   w.Apply(notice with{Status="合成：无法保存确认，请重试"});Layout(w);Save(w,output,"notice.png");
  });
  Check("phone events are ordered and busy clicks rejected", () => {
   List<UiEvent> sent=[];LoginWindow w=new(sent.Add);Model phone=Phone();w.Apply(phone);Layout(w);
   Find<TextBox>(w,"phone").Text="13800000000";Click(w,"send_sms");Click(w,"send_sms");
   Require(sent.Count==1&&sent[0].Kind=="send_sms","duplicate SMS ignored");
   w.Apply(phone with{SmsLabel="58 秒后重发",CanSendSms=false});Layout(w);
   Require(Find<TextBox>(w,"phone").Text=="13800000000","snapshot preserves typed phone");
   Find<TextBox>(w,"captcha").Text="123456";Click(w,"primary");
   Require(sent.Count==2&&sent[1].Kind=="login_phone"&&sent[1].Captcha=="123456"&&sent[1].Remember,"login fields");
   Require(Find<TextBox>(w,"captcha").Text=="","clear submitted captcha");
   w.Apply(phone with{Busy=true});Layout(w);Click(w,"primary");Click(w,"send_sms");Require(sent.Count==2,"busy ignores actions");
   w.Apply(phone with{Status="合成：验证码无效，请重新输入"});Layout(w);Save(w,output,"phone-error.png");
  });
  Check("overlay respects high contrast and disabled transparency independently", () => {
   foreach((bool highContrast,bool transparencyEnabled,byte alpha) in new[]{(false,true,(byte)166),(false,false,(byte)255),(true,true,(byte)255),(true,false,(byte)255)}) {
    LoginWindow w=new(_=>{});w.Apply(Phone());w.UseGameOverlay(highContrast,transparencyEnabled);
    Color color=((SolidColorBrush)((Grid)w.Content).Background).Color;
    Require(color==Color.FromArgb(alpha,0,0,0),$"black mask alpha when highContrast={highContrast}, transparencyEnabled={transparencyEnabled}");
    Layout(w);Save(w,output,$"overlay-preferences-{highContrast}-{transparencyEnabled}.png");
   }
  });
  Check("overlay card remains centered proportional and accessible at client sizes", () => {
   LoginWindow w=new(_=>{});w.Apply(Phone());w.UseGameOverlay();
   FrameworkElement root=(FrameworkElement)w.Content;
   foreach((int width,int height) in new[]{(1280,720),(600,900),(360,240)}) {
    root.Measure(new Size(width,height));root.Arrange(new Rect(0,0,width,height));root.UpdateLayout();
    Border card=Descendants(root).OfType<Border>().Single(x=>AutomationProperties.GetName(x)=="社区/非官方登录窗口");
    Rect bounds=card.TransformToAncestor(root).TransformBounds(new Rect(0,0,card.ActualWidth,card.ActualHeight));
    Require(Math.Abs(bounds.X+bounds.Width/2-width/2.0)<.1&&Math.Abs(bounds.Y+bounds.Height/2-height/2.0)<.1,"card centered in client");
    Require(bounds.Width<=width*.9+.1&&bounds.Height<=height*.8+.1,"card fits 90% width and 80% height");
    Require(Math.Abs(bounds.Width/bounds.Height-420.0/520)<.001,"card keeps design aspect ratio");
    Require(AutomationProperties.GetName(Find<TextBox>(w,"phone"))=="请输入手机号","input remains accessible inside Viewbox");
    Save(w,output,$"overlay-layout-{width}x{height}.png",width,height);
   }
  });
  Check("remember initializes from first snapshot and each screen transition", () => {
   foreach(bool initial in new[]{false,true}) {
    LoginWindow w=new(_=>{});CheckBox remember=Descendants((DependencyObject)w.Content).OfType<CheckBox>().Single();
    remember.IsChecked=!initial;w.Apply(Phone() with{Remember=initial});Layout(w);
    Require(remember.IsChecked==initial,"first phone snapshot initializes remember");
    w.Apply(Accounts() with{Remember=!initial});Layout(w);
    Require(remember.IsChecked==!initial,"different screen uses backend remember snapshot");
    w.Apply(Phone() with{Remember=initial});Layout(w);
    Require(remember.IsChecked==initial,"returning phone screen uses backend remember snapshot");
   }
  });
  Check("SMS countdown preserves pending form state and submits local remember choice", () => {
   foreach(bool initial in new[]{false,true}) {
    List<UiEvent> sent=[];LoginWindow w=new(sent.Add);
    try {
     Model model=Phone() with{Remember=initial};w.Apply(model);w.Show();w.Activate();OverlayTests.Pump();
     TextBox phone=Find<TextBox>(w,"phone"),captcha=Find<TextBox>(w,"captcha");phone.Text="13800000000";captcha.Text="123456";
     CheckBox remember=Descendants((DependencyObject)w.Content).OfType<CheckBox>().Single();remember.IsChecked=!initial;
     foreach(TextBox focused in new[]{phone,captcha}) {
      focused.Focus();focused.Select(2,3);OverlayTests.Pump();Require(focused.IsKeyboardFocused,"input initially focused");
      for(int remaining=59;remaining>=57;remaining--){w.Apply(model with{SmsLabel=remaining+" 秒后重发",CanSendSms=false});OverlayTests.Pump();}
      Require(focused.IsKeyboardFocused,"countdown preserves keyboard focus");
      Require(focused.SelectionStart==2&&focused.SelectionLength==3,"countdown preserves input selection");
      Require(phone.Text=="13800000000"&&captcha.Text=="123456","countdown preserves both unsubmitted text values");
      Require(remember.IsChecked==!initial,"countdown preserves local remember choice");
     }
     Click(w,"primary");Require(sent.Count==1&&sent[0].Kind=="login_phone"&&sent[0].Remember==!initial,"login event carries local remember choice");
     Require(sent[0].Phone=="13800000000"&&sent[0].Captcha=="123456","login event carries retained form values");
    } finally {w.Close();OverlayTests.Pump();}
   }
  });
  Check("saved account rename delete and login stay presentation only", () => {
   List<UiEvent> sent=[];LoginWindow w=new(sent.Add);Model accounts=Accounts();w.Apply(accounts);Layout(w);
   Require(sent.Count==0,"snapshot is not an action");
   Click(w,"rename");Layout(w);Find<TextBox>(w,"account_label").Text="合成改名";Click(w,"confirm_rename");
   Require(sent.Count==1&&sent[0].Kind=="rename_account"&&sent[0].AccountId=="fixture-a"&&sent[0].Label=="合成改名","rename action");
   w.Apply(accounts);Layout(w);Click(w,"delete");Layout(w);Require(sent.Count==1,"delete requires confirmation");Click(w,"confirm_delete");
   Require(sent.Count==2&&sent[1].Kind=="delete_account","confirmed delete");
   w.Apply(accounts);Layout(w);Click(w,"primary");Require(sent.Count==3&&sent[2].Kind=="login_saved","saved login action");
   w.Apply(accounts);Layout(w);Save(w,output,"accounts.png");
  });
  Check("helper process speaks the frame protocol", () => {
   string helper = args.Length > 1 ? args[1] : throw new InvalidOperationException("helper executable path missing");
   Require(File.Exists(helper),"helper exists");
   using Process child = Start(helper);
   Stream input = child.StandardInput.BaseStream, from = child.StandardOutput.BaseStream;
   Envelope? ready = Protocol.Read(from);
   Require(ready is {Type:"event"} && ready.Event!.Kind=="ready","ready event");
   Protocol.Write(input,new Envelope(Type:"model",Model:Notice()));
   Protocol.Write(input,new Envelope(Type:"model",Model:Phone()));
   input.Close();
   Require(Protocol.Read(from) is null,"clean end of frames");
   Require(child.WaitForExit(10000),"helper exit");
   Require(child.ExitCode==0,"helper exit code "+child.ExitCode);
  });
  Check("helper rejects a malformed frame", () => {
   string helper = args.Length > 1 ? args[1] : throw new InvalidOperationException("helper executable path missing");
   using Process child = Start(helper);
   Stream input = child.StandardInput.BaseStream, from = child.StandardOutput.BaseStream;
   Require(Protocol.Read(from)?.Event?.Kind=="ready","ready event");
   byte[] bad = new byte[4+5];BinaryPrimitives.WriteInt32LittleEndian(bad,5);Encoding.UTF8.GetBytes("xxxxx").CopyTo(bad,4);
   input.Write(bad);input.Flush();
   Require(child.WaitForExit(10000),"helper exit");
   Require(child.ExitCode==1,"helper rejects malformed input, got "+child.ExitCode);
  });
  OverlayTests.Run(args[1],output,Check);
  MotionTests.Run(args[1],output,Check);
  ChildTests.Run(args[1],output,Check);
  return Complete(output);
 }
 private static int Complete(string output)
 {
  File.WriteAllLines(Path.Combine(output,"results.txt"),results);
  foreach(string line in results)Console.WriteLine(line);
  return failures==0?0:1;
 }
 private static void Check(string name,Action body){try{body();results.Add("PASS "+name);}catch(Exception e){failures++;results.Add("FAIL "+name+": "+e.GetType().Name+" "+e.Message);}}
 private static void Require(bool condition,string name){if(!condition)throw new InvalidOperationException(name);}
 private static Model Phone()=>new(){Screen="phone",Title="手机号登录",Subtitle="合成离线测试",CanSendSms=true,Remember=true};
 private static Model Accounts()=>new(){Screen="accounts",Title="选择保存的账号",Subtitle="合成离线测试",Accounts=[new("fixture-a","合成账号 A","138****0000"),new("fixture-b","合成账号 B","139****0000")],SelectedAccountId="fixture-a"};
 private static Model Notice()=>new(){Screen="notice",Title="登录前请阅读",Subtitle="合成离线测试",PrimaryLabel="我已了解并同意继续",Notice=Enumerable.Range(1,8).Select(i=>new Section("主题 "+i,"合成告知文字：用途、风险、共享、数据、存储、网络、保留与选择均由 Go 后端提供。这里不保存任何确认状态。")).ToArray()};
 private static void Layout(LoginWindow w){Require(w.Content is FrameworkElement,"window content exists");FrameworkElement content=(FrameworkElement)w.Content;content.Measure(new Size(420,520));content.Arrange(new Rect(0,0,420,520));content.UpdateLayout();}
 private static IEnumerable<DependencyObject> Descendants(DependencyObject root){yield return root;for(int i=0;i<VisualTreeHelper.GetChildrenCount(root);i++)foreach(var x in Descendants(VisualTreeHelper.GetChild(root,i)))yield return x;}
 private static T Find<T>(LoginWindow w,string id)where T:DependencyObject=>Descendants((DependencyObject)w.Content).OfType<T>().First(x=>AutomationProperties.GetAutomationId(x)==id);
 private static void Click(LoginWindow w,string id)=>Find<Button>(w,id).RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
 private static Process Start(string helper)
 {
  ProcessStartInfo info=new(helper) { UseShellExecute=false, RedirectStandardInput=true, RedirectStandardOutput=true, RedirectStandardError=true, CreateNoWindow=true };
  return Process.Start(info) ?? throw new InvalidOperationException("helper start failed");
 }
 private static void Save(LoginWindow w,string dir,string name,int width=420,int height=520){RenderTargetBitmap bitmap=new(width,height,96,96,PixelFormats.Pbgra32);bitmap.Render((Visual)w.Content);PngBitmapEncoder encoder=new();encoder.Frames.Add(BitmapFrame.Create(bitmap));using FileStream file=File.Create(Path.Combine(dir,name));encoder.Save(file);}
}
