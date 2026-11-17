// SPDX-License-Identifier: GPL-3.0-or-later
using System.Collections.Concurrent;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Windows;
using System.Windows.Interop;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Media;
using Oddward.LoginUi;

internal static class ChildTests
{
 internal static void Run(string helper,string output,Action<string,Action> check)
 {
  check("embed protocol preserves canonical handles with strict event fields",()=>{
   foreach(string kind in new[]{"bind_overlay","embed_overlay"}) {
    UiEvent intent=new(kind,Window:"42",GameWindow:"18446744073709551615",RequestId:"7");
    using MemoryStream wire=new();Protocol.Write(wire,new Envelope(Type:"event",Event:intent));wire.Position=0;
    Require(Protocol.Read(wire)!.Event==intent,"binding kind roundtrip");
    using MemoryStream completed=new();Protocol.Write(completed,new Envelope(Type:"window_bound",Event:intent));completed.Position=0;Require(Protocol.Read(completed)==new Envelope(Type:"window_bound",Event:intent),"completion preserves the full binding correlation");
    foreach(UiEvent invalid in new[]{intent with{Window="01"},intent with{GameWindow="0"},intent with{RequestId="0"},intent with{RequestId=null},intent with{RequestId="18446744073709551616"},intent with{Phone="13800000000"},intent with{Captcha="123456"},intent with{Remember=true}}){bool rejected=false;try{using MemoryStream s=new();Protocol.Write(s,new Envelope(Type:"event",Event:invalid));}catch(InvalidDataException){rejected=true;}Require(rejected,"embed rejects malformed handles and unrelated fields");}
   }
  });
  check("embedding waits hidden and native child moves with its parent",()=>{
   Window first=Game("A"),second=Game("B");List<UiEvent> events=[];int? exit=null;
   using LoginViewController controller=new(Application.Current,events.Add,code=>exit=code);
   try {
    first.Show();nint a=new WindowInteropHelper(first).Handle,b=new WindowInteropHelper(second).EnsureHandle();
    Model model=Model();controller.Apply(model);nint own=0;LoginWindow? previous=null;UiEvent? firstRequest=null;ulong lastRequestId=0;
    Require(events.Count==1&&events[0].Kind=="embed_overlay","production requests embed before any direct owner assignment");
    Window[] targets=[first,second,first];nint[] handles=[a,b,a];
    for(int round=0;round<targets.Length;round++) {
     if(round>0){controller.Apply(model with{SmsLabel=(60-round)+" 秒后重发",CanSendSms=false});targets[round].Show();targets[round-1].Hide();}
     int expected=round+1;Until(()=>events.Count(e=>e.Kind=="embed_overlay")==expected,"embed A to B to A round "+expected);
     LoginWindow view=controller.Window!;nint old=own;own=new WindowInteropHelper(view).Handle;
     Require(!view.IsVisible&&!IsWindowVisible(own)&&(GetWindowLongPtrW(own,-16).ToInt64()&0x40000000L)==0,"each binding round starts with a new hidden top-level candidate");
     if(round>0)Require(!ReferenceEquals(previous,view)&&old!=own&&!IsWindow(old)&&IsWindow(a)&&IsWindow(b),"live parent retarget destroys only old UI and allocates a fresh HWND");
     UiEvent request=events.Last(e=>e.Kind=="embed_overlay");firstRequest??=request;ulong id=ulong.Parse(request.RequestId!);Require(id>lastRequestId,"binding IDs increase across controlled recreation");lastRequestId=id;
     Embed(request,Environment.ProcessId,Environment.ProcessId);
     For(180,()=>Require(!IsWindowVisible(own),"native parent alone cannot finish an unacknowledged broker transaction"));
     if(round>0){controller.WindowBound(firstRequest);For(120,()=>Require(!IsWindowVisible(own),"stale first A acknowledgment cannot satisfy later target rounds"));}
     controller.WindowBound(request with{Kind="bind_overlay"});controller.WindowBound(request with{GameWindow="1"});
     For(100,()=>Require(!IsWindowVisible(own),"completion must match kind and both HWNDs"));
     controller.WindowBound(request);
     Until(()=>view.IsVisible&&IsWindowVisible(own)&&ChildOf(own,handles[round])&&Bounds(own)==Client(handles[round]),"native child is shown at exact client bounds");
     TextBox phone=Find<TextBox>(view,"phone"),captcha=Find<TextBox>(view,"captcha");CheckBox remember=Controls((DependencyObject)view.Content).OfType<CheckBox>().Single();
     if(round>0)Require(phone.Text=="13800000000"&&captcha.Text=="123456"&&phone.SelectionStart==2&&phone.SelectionLength==3&&captcha.SelectionStart==1&&captcha.SelectionLength==4&&remember.IsChecked==false,"controlled recreation preserves unsubmitted form and selections");
     if(round>0)Require((string)Find<Button>(view,"send_sms").Content==(60-round)+" 秒后重发"&&!Find<Button>(view,"send_sms").IsEnabled,"controlled recreation uses latest model");
     phone.Text="13800000000";captcha.Text="123456";phone.Select(2,3);captcha.Select(1,4);remember.IsChecked=false;previous=view;
     Rect parentBounds=Bounds(handles[round]);
     for(int i=0;i<20;i++) {
      Require(SetWindowPos(handles[round],0,70+i*3,90+i*2,(int)parentBounds.Width,(int)parentBounds.Height,0x0014),"move only synthetic parent");
      Require(Bounds(own)==Client(handles[round]),"parent-only motion carries child without a polling turn");
     }
     Require(SetWindowPos(handles[round],0,100,120,650,510,0x0014),"resize synthetic parent");
     Until(()=>Bounds(own)==Client(handles[round]),"events resize child to client");
     Require((GetWindowLongPtrW(own,-20).ToInt64()&8)==0,"child is never globally topmost");
    }
    first.Activate();OverlayTests.Pump();For(150,()=>{});OverlayTests.Capture(a,Path.Combine(output,"overlay-native-child.png"));
    Require(exit is null&&!events.Any(e=>e.Kind=="cancel"),"live A to B to A keeps controller alive without cancellation");
   } finally {controller.Dispose();first.Close();second.Close();OverlayTests.Pump();}
  });
  check("pending binding keeps its HWND until ACK even when the game target changes",()=>{
   Window first=Game("pending A"),second=Game("pending B");List<UiEvent> events=[];int? exit=null;
   using LoginViewController controller=new(Application.Current,events.Add,code=>exit=code);
   try {
    first.Show();controller.Apply(Model());LoginWindow old=controller.Window!;UiEvent request=events.Single();nint own=new WindowInteropHelper(old).Handle;
    Embed(request,Environment.ProcessId,Environment.ProcessId,()=>{
     second.Show();first.Hide();
     For(180,()=>Require(ReferenceEquals(controller.Window,old)&&IsWindow(own)&&!IsWindowVisible(own)&&events.Count==1&&exit is null,"target switch during native style stage neither replaces HWND nor starts another transaction"));
    });
    For(180,()=>Require(ReferenceEquals(controller.Window,old)&&IsWindow(own)&&!IsWindowVisible(own)&&events.Count==1,"native parent completion alone keeps pending target transaction hidden"));
    controller.WindowBound(request);Until(()=>events.Count(e=>e.Kind=="embed_overlay")==2,"old acknowledgment permits a fresh candidate for new target");
    Require(!ReferenceEquals(controller.Window,old)&&!IsWindow(own)&&IsWindow(new WindowInteropHelper(first).Handle),"completed transaction replaces only old UI");
    UiEvent next=events.Last();own=Embed(next,Environment.ProcessId,Environment.ProcessId);controller.WindowBound(request);For(100,()=>Require(!IsWindowVisible(own),"old completion cannot acknowledge new target"));controller.WindowBound(next);
    Until(()=>IsWindowVisible(own)&&ChildOf(own,new WindowInteropHelper(second).Handle),"new target appears after its own acknowledgment");
    Require(events.Count==2&&exit is null,"pending target transition emits no cancellation or duplicate transaction");
   } finally {controller.Dispose();first.Close();second.Close();OverlayTests.Pump();}
  });
  check("new overlay HWND matches target DPI context and restores its UI thread context",()=>{
   Window game=Game("system DPI fixture");LoginWindow view=new(_=>{});List<UiEvent> requests=[];int? exit=null;
   using GameOverlay tracker=new(view,code=>exit=code,requestOwner:requests.Add);
   nint before=GetThreadDpiAwarenessContext();
   try {
    nint previous=SetThreadDpiAwarenessContext(new nint(-2));Require(previous!=0,"fixture enters System DPI creation scope");
    nint parent;try{parent=new WindowInteropHelper(game).EnsureHandle();}finally{SetThreadDpiAwarenessContext(previous);}
    game.Show();Require(AreDpiAwarenessContextsEqual(GetWindowDpiAwarenessContext(parent),new nint(-2)),"fixture HWND uses System awareness");
    Model model=Model();view.Apply(model);tracker.Apply(model);nint own=new WindowInteropHelper(view).Handle;
    Require(AreDpiAwarenessContextsEqual(GetWindowDpiAwarenessContext(own),GetWindowDpiAwarenessContext(parent)),"new helper HWND starts with the current game DPI context");
    Require(AreDpiAwarenessContextsEqual(before,GetThreadDpiAwarenessContext()),"EnsureHandle restores original UI thread context");
    Embed(requests.Single(),Environment.ProcessId,Environment.ProcessId);tracker.WindowBound(requests.Single());Until(()=>IsWindowVisible(own)&&Bounds(own)==Client(parent),"matched-context child is visible and aligned");
    view.Apply(model with{SmsLabel="59 秒后重发",CanSendSms=false});For(150,()=>Require(ChildOf(own,parent)&&AreDpiAwarenessContextsEqual(before,GetThreadDpiAwarenessContext()),"model updates retain child and default thread context"));
    Require(exit is null,"DPI-scoped creation remains alive");
   } finally {view.Close();game.Close();OverlayTests.Pump();}
  });
  check("replacement view preserves pending form selection and latest model",()=>{
   Window first=Game("form parent"),second=Game("form replacement");List<UiEvent> events=[];int? exit=null;
   using LoginViewController controller=new(Application.Current,events.Add,code=>exit=code);
   try {
    first.Show();Model model=Model();controller.Apply(model);
    Until(()=>events.Count(e=>e.Kind=="embed_overlay")==1,"form requests initial embed");
    LoginWindow old=controller.Window!;UiEvent firstRequest=events.Last(e=>e.Kind=="embed_overlay");nint own=Embed(firstRequest,Environment.ProcessId,Environment.ProcessId);controller.WindowBound(firstRequest);
    Until(()=>IsWindowVisible(own),"initial form shown");
    TextBox phone=Find<TextBox>(old,"phone"),captcha=Find<TextBox>(old,"captcha");CheckBox remember=Controls((DependencyObject)old.Content).OfType<CheckBox>().Single();
    first.Activate();captcha.Focus();OverlayTests.Pump();Require(captcha.IsKeyboardFocused&&GetFocus()==own,"native child accepts keyboard focus in a text field");
    phone.Text="13800000000";captcha.Text="123456";phone.Select(2,3);captcha.Select(1,4);remember.IsChecked=false;
    bool lossMarkedBeforeClosed=false;old.Closed+=(_,_)=>lossMarkedBeforeClosed=old.InfrastructureLost;
    first.Close();Require(lossMarkedBeforeClosed,"infrastructure loss is marked before Closed observers run");controller.Apply(model with{SmsLabel="59 秒后重发",CanSendSms=false});second.Show();
    OverlayTests.Until(()=>events.Count(e=>e.Kind=="embed_overlay")==2,"replacement form requests embed",()=>"exit="+exit+", events="+string.Join(",",events.Select(e=>e.Kind))+", view="+(controller.Window is null?"null":controller.Window.CloseRequested.ToString()));
    LoginWindow current=controller.Window!;Require(!ReferenceEquals(old,current),"closed Window object is replaced");
    UiEvent replacementRequest=events.Last(e=>e.Kind=="embed_overlay");Require(ulong.Parse(replacementRequest.RequestId!)>ulong.Parse(firstRequest.RequestId!),"request ids increase across Window recreation");
    own=Embed(replacementRequest,Environment.ProcessId,Environment.ProcessId);controller.WindowBound(firstRequest);For(120,()=>Require(!IsWindowVisible(own),"old view completion cannot reveal a replacement"));controller.WindowBound(replacementRequest);Until(()=>IsWindowVisible(own),"replacement form shown");
    phone=Find<TextBox>(current,"phone");captcha=Find<TextBox>(current,"captcha");remember=Controls((DependencyObject)current.Content).OfType<CheckBox>().Single();
    Require(phone.Text=="13800000000"&&captcha.Text=="123456"&&remember.IsChecked==false,"unsubmitted phone captcha and remember survive parent destruction");
    Require(phone.SelectionStart==2&&phone.SelectionLength==3&&captcha.SelectionStart==1&&captcha.SelectionLength==4,"both text selections survive parent destruction");
    Require((string)Find<Button>(current,"send_sms").Content=="59 秒后重发"&&!Find<Button>(current,"send_sms").IsEnabled,"latest model survives recreation");
    captcha.SelectedText="987";Find<Button>(current,"primary").RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
    Require(events.Any(e=>e.Kind=="login_phone"&&e.Phone=="13800000000"&&e.Captcha=="19876"&&!e.Remember),"recreated form submits retained and edited local values");
    current.Close();Until(()=>exit==0,"user close ends controller");
    Require(events.Count(e=>e.Kind=="cancel")==1&&IsWindow(new WindowInteropHelper(second).Handle),"only deliberate user close cancels without destroying parent");
   } finally {controller.Dispose();first.Close();second.Close();OverlayTests.Pump();}
  });
  check("parent destruction rebuilds helper view without cancel or process restart",()=>{
   Window first=Game("destroyed parent"),second=Game("replacement parent");
   using Helper child=new(helper);
   try {
    first.Show();child.Apply(Model());
    UiEvent request=child.NextBinding();nint old=Embed(request,child.Process.Id,Environment.ProcessId);child.Bound(request);
    Until(()=>IsWindowVisible(old),"first child is visible");
    first.Close();OverlayTests.Pump();
    For(180,()=>Require(!child.Process.HasExited&&!child.Events.Any(e=>e.Kind=="cancel"),"destroyed parent does not cancel helper"));
    second.Show();UiEvent replacement=child.NextBinding();nint current=Embed(replacement,child.Process.Id,Environment.ProcessId);Require(ulong.Parse(replacement.RequestId!)>ulong.Parse(request.RequestId!),"helper process does not reuse binding ids after parent destruction");child.Bound(replacement);
    Require(current!=old&&!IsWindow(old),"replacement uses a new live HWND");
    Until(()=>IsWindowVisible(current)&&ChildOf(current,new WindowInteropHelper(second).Handle),"new view embeds into replacement parent");
    child.Close();Require(!child.Events.Any(e=>e.Kind=="cancel"),"server close does not emit user cancellation");
   } finally {first.Close();second.Close();OverlayTests.Pump();}
  });
 }
 private static IEnumerable<DependencyObject> Controls(DependencyObject root){yield return root;for(int i=0;i<VisualTreeHelper.GetChildrenCount(root);i++)foreach(var node in Controls(VisualTreeHelper.GetChild(root,i)))yield return node;}
 private static T Find<T>(LoginWindow window,string id)where T:DependencyObject=>Controls((DependencyObject)window.Content).OfType<T>().Single(x=>AutomationProperties.GetAutomationId(x)==id);
 internal static Model Model(){using Process self=Process.GetCurrentProcess();return new(){Screen="phone",Title="合成原生子窗口",Subtitle="自有父窗口 · 离线生命周期测试",CanSendSms=true,Remember=true,GameProcessId=(uint)Environment.ProcessId,GameCreationTime=self.StartTime.ToUniversalTime().ToFileTimeUtc().ToString(CultureInfo.InvariantCulture)};}
 internal static Window Game(string title)=>new(){Title="Oddward "+title,Width=710,Height=550,Left=80,Top=100,Background=Brushes.CornflowerBlue};
 internal static nint Embed(UiEvent request,int uiPid,int gamePid,Action? staged=null)
 {
  Require(request.Kind=="embed_overlay","fixture accepts only embed intent");
  nint own=(nint)ulong.Parse(request.Window!,CultureInfo.InvariantCulture),parent=(nint)ulong.Parse(request.GameWindow!,CultureInfo.InvariantCulture);
  GetWindowThreadProcessId(own,out uint ui);GetWindowThreadProcessId(parent,out uint game);
  Require(ui==(uint)uiPid&&game==(uint)gamePid&&!IsWindowVisible(own),"fixture binder restricts identities and hidden UI");
  long style=GetWindowLongPtrW(own,-16).ToInt64();
   Require((style&0x40000000L)==0||ChildOf(own,parent),"broker rejects a child belonging to another live parent");
   Require((style&0x10000000L)==0&&(GetWindowLongPtrW(own,-20).ToInt64()&8)==0,"broker requires hidden non-topmost UI styles");
   SetWindowLongPtrW(own,-16,(nint)((style|0x40000000L)&~0x80000000L));
   staged?.Invoke();
   For(180,()=>Require(IsWindow(own)&&!IsWindowVisible(own),"intermediate child style cannot destroy or reveal the pending HWND"));
  SetParent(own,parent);Require(ChildOf(own,parent),"native child binding confirmed");
  GetClientRect(parent,out RECT client);Require(SetWindowPos(own,0,0,0,client.Right,client.Bottom,0x0034),"size synthetic child");return own;
 }
 internal static bool ChildOf(nint own,nint parent){long style=GetWindowLongPtrW(own,-16).ToInt64();return GetParent(own)==parent&&(style&0x40000000L)!=0&&(style&0x80000000L)==0;}
 internal static Rect Bounds(nint hwnd){GetWindowRect(hwnd,out RECT rect);return new Rect(rect.Left,rect.Top,rect.Right-rect.Left,rect.Bottom-rect.Top);}
 internal static Rect Client(nint hwnd){GetClientRect(hwnd,out RECT rect);POINT p=new();ClientToScreen(hwnd,ref p);return new Rect(p.X,p.Y,rect.Right,rect.Bottom);}
 internal static void Until(Func<bool> condition,string message)=>OverlayTests.Until(condition,message);
 internal static void For(int milliseconds,Action action){Stopwatch watch=Stopwatch.StartNew();while(watch.ElapsedMilliseconds<milliseconds){OverlayTests.Pump();action();Thread.Sleep(5);}}
 internal static void Require(bool condition,string message){if(!condition)throw new InvalidOperationException(message);}
 private sealed class Helper:IDisposable
 {
  internal Process Process{get;}
  internal ConcurrentQueue<UiEvent> Events{get;}=new();
  private readonly ConcurrentQueue<UiEvent> requests=new();
  private readonly Task reader;
  internal Helper(string path) {
   Process=System.Diagnostics.Process.Start(new ProcessStartInfo(path){UseShellExecute=false,RedirectStandardInput=true,RedirectStandardOutput=true,CreateNoWindow=true})!;
   reader=Task.Run(()=>{while(Protocol.Read(Process.StandardOutput.BaseStream) is Envelope frame){UiEvent e=frame.Event!;Events.Enqueue(e);if(e.Kind is "embed_overlay" or "bind_overlay")requests.Enqueue(e);}});
   Until(()=>Events.Any(e=>e.Kind=="ready"),"child helper ready");
  }
  internal void Bound(UiEvent request)=>Protocol.Write(Process.StandardInput.BaseStream,new Envelope(Type:"window_bound",Event:request));
  internal void Apply(Model model)=>Protocol.Write(Process.StandardInput.BaseStream,new Envelope(Type:"model",Model:model));
  internal UiEvent NextBinding(){Until(()=>!requests.IsEmpty||Process.HasExited,"binding request arrives");Require(requests.TryDequeue(out UiEvent? e),"helper remains alive to request embed");return e!;}
  internal void Close(){if(!Process.HasExited){Protocol.Write(Process.StandardInput.BaseStream,new Envelope(Type:"close"));Until(()=>Process.HasExited,"server close exits");}Require(Process.ExitCode==0,"helper exit is successful");Until(()=>reader.IsCompleted,"event reader completes");reader.GetAwaiter().GetResult();}
  public void Dispose(){try{if(!Process.HasExited){Process.StandardInput.Close();try{Until(()=>Process.HasExited,"fixture helper EOF closes");}finally{if(!Process.HasExited){Process.Kill();Process.WaitForExit(5000);}}}}finally{Process.Dispose();}}
 }
 [StructLayout(LayoutKind.Sequential)]private struct RECT{internal int Left,Top,Right,Bottom;}
 [StructLayout(LayoutKind.Sequential)]private struct POINT{internal int X,Y;}
 [DllImport("user32.dll")]private static extern nint GetThreadDpiAwarenessContext();
 [DllImport("user32.dll")]private static extern nint SetThreadDpiAwarenessContext(nint context);
 [DllImport("user32.dll")]private static extern nint GetWindowDpiAwarenessContext(nint window);
 [DllImport("user32.dll")]private static extern bool AreDpiAwarenessContextsEqual(nint first,nint second);
 [DllImport("user32.dll")]private static extern bool GetWindowRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool GetClientRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool ClientToScreen(nint hwnd,ref POINT point);
 [DllImport("user32.dll")]private static extern uint GetWindowThreadProcessId(nint hwnd,out uint pid);
 [DllImport("user32.dll")]private static extern bool IsWindowVisible(nint hwnd);
 [DllImport("user32.dll")]private static extern bool IsWindow(nint hwnd);
 [DllImport("user32.dll")]private static extern nint GetParent(nint hwnd);
 [DllImport("user32.dll")]private static extern nint GetFocus();
 [DllImport("user32.dll")]private static extern nint SetParent(nint hwnd,nint parent);
 [DllImport("user32.dll")]private static extern nint GetWindowLongPtrW(nint hwnd,int index);
 [DllImport("user32.dll")]private static extern nint SetWindowLongPtrW(nint hwnd,int index,nint value);
 [DllImport("user32.dll")]private static extern bool SetWindowPos(nint hwnd,nint after,int x,int y,int width,int height,uint flags);
}
