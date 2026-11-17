// SPDX-License-Identifier: GPL-3.0-or-later
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Interop;
using System.Windows.Media;
using System.Windows.Threading;
using Oddward.LoginUi;

internal static class OverlayTests
{
 internal static void Run(string helper,string output,Action<string,Action> check)
 {
  check("bound helper owns and covers only the synthetic game client",()=>{
   Window game=new(){Title="Oddward synthetic game fixture",Width=1000,Height=720,Left=70,Top=80,Background=Brushes.CornflowerBlue};
   try {
    game.Show();Pump();nint gameHwnd=new WindowInteropHelper(game).Handle;
    using Helper child=new(helper);
    child.Model(new Model{Screen="phone",Title="合成覆盖层",Subtitle="自有窗口 · 离线测试",GameProcessId=(uint)Environment.ProcessId,GameCreationTime=CreationTime()});
    nint overlay=0;Until(()=>{overlay=VisibleWindow((uint)child.Process.Id);return overlay!=0;},"overlay appears");
    Require(GetWindow(overlay,4)==gameHwnd,"overlay owner is synthetic game HWND");
    Until(()=>WindowBounds(overlay)==ClientBounds(gameHwnd),"overlay covers game client pixels");
    Require((GetWindowLongPtrW(overlay,-20).ToInt64()&8)==0,"overlay is not globally topmost");
    Require(IsAbove(overlay,gameHwnd),"owned overlay stays above game");
    Require(AreDpiAwarenessContextsEqual(GetWindowDpiAwarenessContext(overlay),new nint(-4)),"helper uses PerMonitorV2 awareness");
    game.Left=180;game.Top=120;game.Width=760;game.Height=560;
    Until(()=>WindowBounds(overlay)==ClientBounds(gameHwnd),"overlay follows move and client resize");
    game.WindowState=WindowState.Minimized;
    Until(()=>!IsWindowVisible(overlay),"overlay hides with minimized game");
    game.WindowState=WindowState.Normal;
    Until(()=>IsWindowVisible(overlay)&&WindowBounds(overlay)==ClientBounds(gameHwnd),"overlay restores with game");
    Window other=new(){Title="Oddward unrelated fixture",Width=300,Height=200,Left=160,Top=140};
    try {
     other.Show();other.Activate();Pump();nint otherHwnd=new WindowInteropHelper(other).Handle;
     Until(()=>GetForegroundWindow()==otherHwnd,"unrelated fixture can become foreground");
     For(250,()=>Require(GetForegroundWindow()==otherHwnd,"tracking does not steal foreground"));
     Require(IsAbove(otherHwnd,overlay),"unrelated foreground window stays above overlay");
    } finally {other.Close();Pump();}
    game.Activate();Pump();
    Until(()=>GetForegroundWindow()==gameHwnd||GetWindow(GetForegroundWindow(),4)==gameHwnd,"fixture group active for screenshot");
    For(200,()=>{});
    Capture(gameHwnd,Path.Combine(output,"overlay-client.png"));
    game.Left=-120;game.Top=-40;
    Until(()=>WindowBounds(overlay)==ClientBounds(gameHwnd),"signed negative client origin follows");
    List<RECT> monitors=[];EnumDisplayMonitors(0,0,(nint monitor,nint dc,ref RECT rect,nint data)=>{monitors.Add(rect);return true;},0);
    Require(monitors.Count>0,"display monitor available");List<string> dpiEvidence=[];
    foreach(RECT monitor in monitors) {
     Require(SetWindowPos(gameHwnd,0,monitor.Left+40,monitor.Top+40,Math.Min(900,monitor.Right-monitor.Left-80),Math.Min(650,monitor.Bottom-monitor.Top-80),0x0014),"move only synthetic fixture between monitors");
     Until(()=>WindowBounds(overlay)==ClientBounds(gameHwnd)&&GetDpiForWindow(overlay)==GetDpiForWindow(gameHwnd),"overlay follows monitor DPI and physical client bounds");
     dpiEvidence.Add($"Monitor {monitor.Left},{monitor.Top}: game DPI {GetDpiForWindow(gameHwnd)}, overlay DPI {GetDpiForWindow(overlay)}, client {ClientBounds(gameHwnd)}, overlay {WindowBounds(overlay)}");
    }
    File.WriteAllLines(Path.Combine(output,"overlay-monitors.txt"),dpiEvidence);
   } finally {game.Close();Pump();}
  });
  check("rejected native owner waits hidden for one binding request and verified native owner",()=>{
   using Process game=StartFixture();LoginWindow window=new(_=>{});int? exit=null;int attempts=0;List<UiEvent> requests=[];
   using GameOverlay tracker=new(window,code=>exit=code,(interop,owner)=>{attempts++;RejectNativeOwner(interop,owner);},requests.Add,preferEmbedding:false);
   try {
    nint gameHwnd=VisibleWindow((uint)game.Id);ActivateFixture(gameHwnd);
    Model model=new(){Screen="phone",Title="合成绑定覆盖层",Subtitle="等待受限原生绑定 · 离线测试",Remember=true,CanSendSms=true,GameProcessId=(uint)game.Id,GameCreationTime=CreationTime(game)};
    window.Apply(model);tracker.Apply(model);nint overlay=new WindowInteropHelper(window).Handle;
    Require(!IsWindowVisible(overlay)&&!window.IsVisible,"rejected binding never flashes a standalone window");
    Until(()=>requests.Count==1,"one native owner binding requested");
    For(350,()=>Require(!IsWindowVisible(overlay)&&requests.Count==1&&attempts==1,"delayed binder stays hidden without repeat requests"));
    Window incorrectOwner=new(){ShowInTaskbar=false};
    try {
     nint wrong=new WindowInteropHelper(incorrectOwner).EnsureHandle();SetWindowLongPtrW(overlay,-8,wrong);
     Require(GetWindow(overlay,4)==wrong,"synthetic wrong owner installed");
     For(250,()=>Require(!IsWindowVisible(overlay)&&requests.Count==1,"wrong native owner cannot reveal overlay"));
     new WindowInteropHelper(window).Owner=0;
     BindRequested(requests.Single(),window,game,tracker);
     Until(()=>IsWindowVisible(overlay)&&IsAbove(overlay,gameHwnd),"confirmed owner reveals overlay above game",()=>BindingState(window,gameHwnd,exit));
     Require(new WindowInteropHelper(window).Owner==gameHwnd,"managed owner cache agrees with native binding");
    } finally {incorrectOwner.Close();}
    Require(WindowBounds(overlay)==ClientBounds(gameHwnd),"bound overlay covers only game client");
    Require((GetWindowLongPtrW(overlay,-20).ToInt64()&8)==0,"bound overlay is not globally topmost");
    Require(SetWindowPos(gameHwnd,0,180,130,710,550,0x0014),"resize only self-created game fixture");
    Until(()=>WindowBounds(overlay)==ClientBounds(gameHwnd),"bound overlay follows physical move and resize");
    using(Process other=StartFixture(gameHwnd))try {
     nint otherHwnd=VisibleWindow((uint)other.Id);ActivateFixture(otherHwnd);
     For(250,()=>Require(GetForegroundWindow()==otherHwnd&&IsAbove(otherHwnd,overlay),"bound overlay stays below unrelated foreground app"));
     FixtureCommand(other,"activate-target");Until(()=>GetForegroundWindow()==gameHwnd||GetForegroundWindow()==overlay,"fixture switch brings game group to foreground");
     Until(()=>IsWindowVisible(overlay)&&IsAbove(overlay,gameHwnd),"bound overlay returns with game",()=>BindingState(window,gameHwnd,exit));
    } finally {CloseFixture(other);}
    For(250,()=>Require(IsWindowVisible(overlay)&&IsAbove(overlay,gameHwnd)&&requests.Count==1,"native owned overlay stays above game without repeat requests"));
    Capture(gameHwnd,Path.Combine(output,"overlay-bound-client.png"));
    Require(exit is null,"binding does not fail the helper");
   } finally {window.Close();CloseFixture(game);Pump();}
  });
  check("owner binding requests renew across live game windows A to B to A",()=>{
   using Process game=Process.GetCurrentProcess();
   Window first=new(){Title="Oddward synthetic game A",Width=780,Height=580,Background=Brushes.CornflowerBlue};
   Window second=new(){Title="Oddward synthetic game B",Width=650,Height=500,Background=Brushes.CornflowerBlue};
   LoginWindow window=new(_=>{});int? exit=null;int attempts=0;List<UiEvent> requests=[];
   using GameOverlay tracker=new(window,code=>exit=code,(interop,owner)=>{attempts++;RejectNativeOwner(interop,owner);},requests.Add,preferEmbedding:false);
   try {
    first.Show();Pump();nint firstHwnd=new WindowInteropHelper(first).Handle,secondHwnd=new WindowInteropHelper(second).EnsureHandle();
    Model model=new(){Screen="phone",GameProcessId=(uint)game.Id,GameCreationTime=CreationTime(game)};
    window.Apply(model);tracker.Apply(model);nint overlay=new WindowInteropHelper(window).Handle;
    Window[] targets=[first,second,first];nint[] handles=[firstHwnd,secondHwnd,firstHwnd];
    for(int round=0;round<targets.Length;round++) {
     if(round>0){targets[round].Show();targets[round-1].Hide();}
     int expected=round+1;nint target=handles[round];
     Until(()=>requests.Count==expected,$"binding request for A to B to A round {expected}");
     Require(attempts==expected,"native owner attempted once per target round");
     For(250,()=>Require(!IsWindowVisible(overlay)&&requests.Count==expected&&attempts==expected,"stable target waits hidden without repeating its request"));
     if(round>0)Require(GetWindow(overlay,4)==handles[round-1],"rejected owner retains the previous live native owner");
     Require(new WindowInteropHelper(first).Handle==firstHwnd&&new WindowInteropHelper(second).Handle==secondHwnd,"both game HWNDs remain alive through target switches");
     BindRequested(requests[round],window,game,tracker);
     Until(()=>GetWindow(overlay,4)==target&&IsWindowVisible(overlay)&&IsAbove(overlay,target)&&WindowBounds(overlay)==ClientBounds(target),$"bound owner and geometry restored in round {expected}");
     For(150,()=>Require(requests.Count==expected&&attempts==expected,"successful target binding does not repeat"));
    }
    Require(exit is null,"target round changes keep helper alive");
   } finally {window.Close();first.Close();second.Close();Pump();}
  });
  check("confirmed owner preserves editable form through countdown minimization and app switching",()=>{
   using Process game=StartFixture();LoginWindow window=new(_=>{});int? exit=null;List<UiEvent> requests=[];
   using GameOverlay tracker=new(window,code=>exit=code,RejectNativeOwner,requests.Add,preferEmbedding:false);
   try {
    nint gameHwnd=VisibleWindow((uint)game.Id);ActivateFixture(gameHwnd);
    Model model=new(){Screen="phone",Title="合成输入保留",Remember=true,CanSendSms=true,GameProcessId=(uint)game.Id,GameCreationTime=CreationTime(game)};
    window.Apply(model);tracker.Apply(model);nint overlay=new WindowInteropHelper(window).Handle;
    Until(()=>requests.Count==1,"editable overlay requests restricted binding");
    BindRequested(requests.Single(),window,game,tracker);
    Until(()=>IsWindowVisible(overlay)&&IsAbove(overlay,gameHwnd),"editable bound overlay appears",()=>BindingState(window,gameHwnd,exit));
    TextBox phone=FindControl<TextBox>(window,"phone"),captcha=FindControl<TextBox>(window,"captcha");
    CheckBox remember=Descendants((DependencyObject)window.Content).OfType<CheckBox>().Single();
    phone.Text="13800000000";captcha.Text="123456";remember.IsChecked=false;
    window.Activate();captcha.Focus();captcha.Select(2,3);Pump();
    Until(()=>GetForegroundWindow()==overlay&&captcha.IsKeyboardFocused,"bound overlay input gains keyboard focus");
    For(250,()=>Require(captcha.IsKeyboardFocused&&IsAbove(overlay,gameHwnd),"tracking keeps input focused above game"));
    window.Apply(model with{SmsLabel="59 秒后重发",CanSendSms=false});Pump();
    Require(captcha.IsKeyboardFocused&&captcha.SelectionStart==2&&captcha.SelectionLength==3,"bound overlay countdown retains focus and selection");
    FixtureCommand(game,"minimize");Until(()=>!IsWindowVisible(overlay),"bound overlay hides with minimized game");
    FixtureCommand(game,"restore");ActivateFixture(gameHwnd,"after minimization");Until(()=>IsWindowVisible(overlay)&&IsAbove(overlay,gameHwnd),"bound overlay restores after minimization");
    Require(phone.Text=="13800000000"&&captcha.Text=="123456"&&remember.IsChecked==false,"minimization retains pending form");
    using(Process other=StartFixture(gameHwnd))try {
     nint otherHwnd=VisibleWindow((uint)other.Id);ActivateFixture(otherHwnd);
     For(250,()=>Require(GetForegroundWindow()==otherHwnd&&IsAbove(otherHwnd,overlay),"editable bound overlay yields to unrelated app"));
     FixtureCommand(other,"activate-target");Until(()=>GetForegroundWindow()==gameHwnd||GetForegroundWindow()==overlay,"fixture switch brings game group to foreground");Until(()=>IsWindowVisible(overlay)&&IsAbove(overlay,gameHwnd),"editable bound overlay returns with game");
    } finally {CloseFixture(other);}
    window.Activate();captcha.Focus();Pump();
    Require(captcha.IsKeyboardFocused&&captcha.SelectionStart==2&&captcha.SelectionLength==3,"restored input recovers keyboard focus and selection");
    captcha.SelectedText="987";
    Require(phone.Text=="13800000000"&&captcha.Text=="129876"&&remember.IsChecked==false,"restored input accepts editing without resetting form");
    Require((GetWindowLongPtrW(overlay,-20).ToInt64()&8)==0&&exit is null,"editable bound overlay remains ordinary and alive");
   } finally {window.Close();CloseFixture(game);Pump();}
  });
  check("helper waits hidden until its identified game has a visible window",()=>{
   using Helper child=new(helper);
   For(150,()=>Require(VisibleWindow((uint)child.Process.Id)==0,"no pre-model desktop flash"));
   child.Model(new Model{Screen="phone",GameProcessId=(uint)Environment.ProcessId,GameCreationTime=CreationTime()});
   For(250,()=>Require(VisibleWindow((uint)child.Process.Id)==0&&!child.Process.HasExited,"no unrelated bound overlay while waiting"));
   Window game=new(){Title="Oddward delayed fixture",Width=650,Height=500};
   try{game.Show();Pump();nint gameHwnd=new WindowInteropHelper(game).Handle;Until(()=>GetWindow(VisibleWindow((uint)child.Process.Id),4)==gameHwnd,"late game window bound");}
   finally{game.Close();Pump();}
  });
  check("helper stays alive across hidden game window and observes process exit code 259",()=>{
   using Process game=StartFixture();
   try {
    using Helper child=new(helper);
    child.Model(new Model{Screen="phone",GameProcessId=(uint)game.Id,GameCreationTime=CreationTime(game)});
    Until(()=>VisibleWindow((uint)child.Process.Id)!=0,"overlay attached before process exit");
    FixtureCommand(game,"hide");
    Until(()=>VisibleWindow((uint)child.Process.Id)==0,"overlay hides with game window");
    For(250,()=>Require(!game.HasExited&&!child.Process.HasExited,"hidden live process is not an exited process"));
    FixtureCommand(game,"show");
    Until(()=>VisibleWindow((uint)child.Process.Id)!=0,"overlay restores while same game process lives");
    Require(GetWindow(VisibleWindow((uint)child.Process.Id),4)==VisibleWindow((uint)game.Id),"restored overlay retains game owner");
    game.StandardInput.WriteLine("exit-259");game.StandardInput.Flush();
    Until(()=>game.HasExited,"synthetic game exits with STILL_ACTIVE numeric value");Require(game.ExitCode==259,"fixture exits with requested code");
    Until(()=>child.Process.HasExited,"signaled game handle closes overlay even for exit code 259");Require(child.Process.ExitCode==0,"game exit closes helper successfully");
   } finally {if(!game.HasExited){game.Kill();game.WaitForExit(5000);}}
  });
  check("helper observes game process exit while waiting for a visible window",()=>{
   using Process game=StartFixture();
   try {
    FixtureCommand(game,"hide");
    using Helper child=new(helper);
    child.Model(new Model{Screen="phone",GameProcessId=(uint)game.Id,GameCreationTime=CreationTime(game)});
    For(250,()=>Require(!child.Process.HasExited&&VisibleWindow((uint)child.Process.Id)==0,"waiting helper remains alive and hidden"));
    game.StandardInput.Close();Until(()=>game.HasExited,"hidden synthetic game exits");
    Until(()=>child.Process.HasExited,"hidden waiting helper exits with game");Require(child.Process.ExitCode==0,"exit without a bound HWND closes successfully");
   } finally {if(!game.HasExited){game.Kill();game.WaitForExit(5000);}}
  });
  check("helper rejects stale creation identity without displaying a window",()=>{
   using Helper child=new(helper);
   child.Model(new Model{Screen="phone",GameProcessId=(uint)Environment.ProcessId,GameCreationTime="1"});
   Until(()=>child.Process.HasExited,"stale identity exits");Require(child.Process.ExitCode==1,"stale identity fails closed");
  });
 }
 internal static int FixtureMain()
 {
  Application app=new(){ShutdownMode=ShutdownMode.OnExplicitShutdown};
  nint.TryParse(Environment.GetEnvironmentVariable("ODDWARD_TEST_FOREGROUND_TARGET"),out nint foregroundTarget);
  Window game=new(){Title="Oddward exit fixture",Width=800,Height=600,Left=100,Top=100,Background=Brushes.CornflowerBlue};
  app.Startup+=(_,_)=>{
   game.Show();Console.WriteLine("ready");Console.Out.Flush();
   Thread reader=new(()=>{
    while(Console.ReadLine() is string command)app.Dispatcher.BeginInvoke(()=>{
     if(command=="exit-259"){app.Shutdown(259);return;}
     if(command=="hide")game.Hide();else if(command=="show")game.Show();
     else if(command=="minimize")game.WindowState=WindowState.Minimized;
     else if(command=="restore")game.WindowState=WindowState.Normal;
     else if(command=="activate-target"&&foregroundTarget!=0) {
      // The currently foreground fixture performs the user-like app switch;
      // a background test runner has no entitlement to activate another app.
      if(GetForegroundWindow()!=new WindowInteropHelper(game).Handle||!SetForegroundWindow(foregroundTarget)){Console.WriteLine("activation-rejected");Console.Out.Flush();return;}
     }
     else{app.Shutdown(1);return;}
     Console.WriteLine(command);Console.Out.Flush();
    });
    app.Dispatcher.BeginInvoke(()=>app.Shutdown());
   }){IsBackground=true};reader.Start();
  };
  return app.Run();
 }
 private static Process StartFixture(nint foregroundTarget=0)
 {
  ProcessStartInfo start=new(Environment.ProcessPath!){UseShellExecute=false,RedirectStandardInput=true,RedirectStandardOutput=true,CreateNoWindow=true};start.ArgumentList.Add("--game-fixture");
  start.Environment["ODDWARD_TEST_FOREGROUND_TARGET"]=foregroundTarget.ToString(System.Globalization.CultureInfo.InvariantCulture);
  Process game=Process.Start(start)!;
  try{Task<string?> ready=game.StandardOutput.ReadLineAsync();Require(ready.Wait(5000)&&ready.Result=="ready","game fixture ready");return game;}
  catch{try{if(!game.HasExited){game.Kill();game.WaitForExit(5000);}}finally{game.Dispose();}throw;}
 }
 private static void FixtureCommand(Process game,string command)
 {
  game.StandardInput.WriteLine(command);game.StandardInput.Flush();
  Task<string?> response=game.StandardOutput.ReadLineAsync();Until(()=>response.IsCompleted,"game fixture responds to "+command);
  Require(response.IsCompletedSuccessfully&&response.Result==command,"game fixture acknowledges "+command);
 }
 private static void BindRequested(UiEvent request,LoginWindow window,Process game,GameOverlay tracker)
 {
  // Synthetic binder accepts only this test's live game and our known UI HWND.
  using MemoryStream wire=new();Protocol.Write(wire,new Envelope(Type:"event",Event:request));wire.Position=0;
  request=Protocol.Read(wire)!.Event!;
  Require(request.Kind=="bind_overlay"&&!game.HasExited,"binding request is for a live synthetic game");
  nint own=new WindowInteropHelper(window).Handle,gameHwnd=VisibleWindow((uint)game.Id);
  Require(request.Window==((ulong)(nuint)own).ToString(System.Globalization.CultureInfo.InvariantCulture)&&request.GameWindow==((ulong)(nuint)gameHwnd).ToString(System.Globalization.CultureInfo.InvariantCulture),"binding carries only the exact synthetic HWND pair");
  GetWindowThreadProcessId(own,out uint ownPid);GetWindowThreadProcessId(gameHwnd,out uint gamePid);
  Require(ownPid==(uint)Environment.ProcessId&&gamePid==(uint)game.Id,"binder restricts both process identities");
  Marshal.SetLastPInvokeError(0);nint previous=SetWindowLongPtrW(own,-8,gameHwnd);int error=Marshal.GetLastPInvokeError();
  Require((previous!=0||error==0)&&GetWindow(own,4)==gameHwnd,"binder installs and reads back native game owner");
  tracker.WindowBound(request);
 }
 private static void RejectNativeOwner(WindowInteropHelper interop,nint owner)
 {
  // Reproduce WPF's cached-owner/native-owner mismatch without elevated processes.
  nint hiddenOwner=GetWindow(interop.Handle,4);interop.Owner=owner;
  SetWindowLongPtrW(interop.Handle,-8,hiddenOwner);
  Require(interop.Owner==owner&&GetWindow(interop.Handle,4)==hiddenOwner&&hiddenOwner!=owner,"inject cached owner with rejected native ownership");
 }
 private static void ActivateFixture(nint hwnd,string stage=""){nint before=GetForegroundWindow();bool activated=hwnd!=0&&SetForegroundWindow(hwnd);Pump();Require(activated,$"activate self-created fixture {stage}: target={hwnd}, before={before}, after={GetForegroundWindow()}, afterOwner={GetWindow(GetForegroundWindow(),4)}");Until(()=>GetForegroundWindow()==hwnd,"fixture reaches foreground "+stage);}
 private static void CloseFixture(Process game){if(game.HasExited)return;try{game.StandardInput.Close();Until(()=>game.HasExited,"synthetic fixture closes");}finally{if(!game.HasExited){game.Kill();game.WaitForExit(5000);}}}
 private static IEnumerable<DependencyObject> Descendants(DependencyObject root){yield return root;for(int i=0;i<VisualTreeHelper.GetChildrenCount(root);i++)foreach(var child in Descendants(VisualTreeHelper.GetChild(root,i)))yield return child;}
 private static T FindControl<T>(LoginWindow window,string id)where T:DependencyObject=>Descendants((DependencyObject)window.Content).OfType<T>().Single(x=>AutomationProperties.GetAutomationId(x)==id);
 private static string CreationTime(){using Process current=Process.GetCurrentProcess();return CreationTime(current);}
 private static string CreationTime(Process process)=>process.StartTime.ToUniversalTime().ToFileTimeUtc().ToString(System.Globalization.CultureInfo.InvariantCulture);
 internal static void Pump(){DispatcherFrame frame=new();Dispatcher.CurrentDispatcher.BeginInvoke(DispatcherPriority.Background,()=>frame.Continue=false);Dispatcher.PushFrame(frame);}
 internal static void Until(Func<bool> predicate,string message,Func<string>? detail=null){Stopwatch watch=Stopwatch.StartNew();while(watch.ElapsedMilliseconds<5000){Pump();if(predicate())return;Thread.Sleep(15);}throw new InvalidOperationException(message+(detail is null?"":"; "+detail()));}
 private static string BindingState(LoginWindow window,nint game,int? exit){nint own=new WindowInteropHelper(window).Handle;return $"visible={IsWindowVisible(own)}, wpfVisible={window.IsVisible}, foreground={GetForegroundWindow()}, own={own}, game={game}, nativeOwner={GetWindow(own,4)}, above={IsAbove(own,game)}, topmost={GetWindowLongPtrW(own,-20).ToInt64()&8}/{GetWindowLongPtrW(game,-20).ToInt64()&8}, exit={exit}, bounds={WindowBounds(own)}, client={ClientBounds(game)}";}
 private static void For(int milliseconds,Action action){Stopwatch watch=Stopwatch.StartNew();while(watch.ElapsedMilliseconds<milliseconds){Pump();action();Thread.Sleep(15);}}
 private static bool IsAbove(nint higher,nint lower){for(nint hwnd=lower;hwnd!=0;hwnd=GetWindow(hwnd,3))if(hwnd==higher)return true;return false;}
 internal static void Capture(nint game,string path)
 {
  Rect bounds=ClientBounds(game);int width=(int)bounds.Width,height=(int)bounds.Height;
  nint screen=GetDC(0),memory=CreateCompatibleDC(screen),bitmap=CreateCompatibleBitmap(screen,width,height),old=SelectObject(memory,bitmap);
  try {
   Require(BitBlt(memory,0,0,width,height,screen,(int)bounds.X,(int)bounds.Y,0x40CC0020),"capture synthetic client");
   var source=Imaging.CreateBitmapSourceFromHBitmap(bitmap,0,Int32Rect.Empty,System.Windows.Media.Imaging.BitmapSizeOptions.FromEmptyOptions());
   var encoder=new System.Windows.Media.Imaging.PngBitmapEncoder();encoder.Frames.Add(System.Windows.Media.Imaging.BitmapFrame.Create(source));
   using FileStream file=File.Create(path);encoder.Save(file);
   byte[] pixel=new byte[4];source.CopyPixels(new Int32Rect(12,12,1,1),pixel,4,0);
   Require(pixel[0]<120&&pixel[1]<100&&pixel[2]<70,"black mask visibly dims synthetic blue client");
  } finally {SelectObject(memory,old);DeleteObject(bitmap);DeleteDC(memory);ReleaseDC(0,screen);}
 }
 private static void Require(bool value,string message){if(!value)throw new InvalidOperationException(message);}
 internal static nint VisibleWindow(uint pid){nint found=0;EnumWindows((hwnd,_)=>{GetWindowThreadProcessId(hwnd,out uint owner);if(owner==pid&&IsWindowVisible(hwnd)){found=hwnd;return false;}return true;},0);return found;}
 private static Rect WindowBounds(nint hwnd){GetWindowRect(hwnd,out RECT r);return new Rect(r.Left,r.Top,r.Right-r.Left,r.Bottom-r.Top);}
 private static Rect ClientBounds(nint hwnd){GetClientRect(hwnd,out RECT r);POINT p=new();ClientToScreen(hwnd,ref p);return new Rect(p.X,p.Y,r.Right,r.Bottom);}
 internal static void BindFixtureOwner(UiEvent request,int uiPid,uint gamePid)
 {
  Require(request.Kind is "bind_overlay" or "embed_overlay","synthetic controller accepts binding kinds");
  nint own=(nint)ulong.Parse(request.Window!,System.Globalization.CultureInfo.InvariantCulture),parent=(nint)ulong.Parse(request.GameWindow!,System.Globalization.CultureInfo.InvariantCulture);
  GetWindowThreadProcessId(own,out uint ownPid);GetWindowThreadProcessId(parent,out uint parentPid);
  Require(ownPid==(uint)uiPid&&parentPid==gamePid&&gamePid!=0&&!IsWindowVisible(own),"synthetic owner binder restricts hidden helper and fixture identities");
  SetWindowLongPtrW(own,-8,parent);Require(GetWindow(own,4)==parent,"synthetic owner readback matches");
 }
 private sealed class Helper:IDisposable
 {
  internal Process Process {get;}
  private readonly object writes=new();
  private readonly Task events;
  private uint gamePid;
  internal Helper(string path){
   Process=Process.Start(new ProcessStartInfo(path){UseShellExecute=false,RedirectStandardInput=true,RedirectStandardOutput=true,RedirectStandardError=true,CreateNoWindow=true})!;
   Task<Envelope?> ready=Task.Run(()=>Protocol.Read(Process.StandardOutput.BaseStream));Require(ready.Wait(5000)&&ready.Result?.Event?.Kind=="ready","helper ready");
   events=Task.Run(()=>{while(Protocol.Read(Process.StandardOutput.BaseStream) is Envelope frame)if(frame.Event is {Kind:"bind_overlay" or "embed_overlay"} request){BindFixtureOwner(request,Process.Id,Volatile.Read(ref gamePid));Send(new Envelope(Type:"window_bound",Event:request));}});
  }
  private void Send(Envelope frame){lock(writes)Protocol.Write(Process.StandardInput.BaseStream,frame);}
  internal void Model(Model value){Volatile.Write(ref gamePid,value.GameProcessId);Send(new Envelope(Type:"model",Model:value));}
  public void Dispose(){try{if(!Process.HasExited){try{Send(new Envelope(Type:"close"));}catch(IOException){}try{Until(()=>Process.HasExited,"helper close exits cleanly");}finally{if(!Process.HasExited){Process.Kill();Process.WaitForExit(5000);}}}Until(()=>events.IsCompleted,"helper event reader completes");events.GetAwaiter().GetResult();}finally{Process.Dispose();}}
 }
 [StructLayout(LayoutKind.Sequential)]private struct RECT{internal int Left,Top,Right,Bottom;}
 [StructLayout(LayoutKind.Sequential)]private struct POINT{internal int X,Y;}
 private delegate bool EnumProc(nint hwnd,nint data);
 private delegate bool MonitorProc(nint monitor,nint dc,ref RECT rect,nint data);
 [DllImport("user32.dll")]private static extern bool EnumDisplayMonitors(nint dc,nint clip,MonitorProc callback,nint data);
 [DllImport("user32.dll")]private static extern uint GetDpiForWindow(nint hwnd);
 [DllImport("user32.dll")]private static extern nint GetWindowDpiAwarenessContext(nint hwnd);
 [DllImport("user32.dll")]private static extern bool AreDpiAwarenessContextsEqual(nint a,nint b);
 [DllImport("user32.dll")]private static extern bool SetWindowPos(nint hwnd,nint insertAfter,int x,int y,int width,int height,uint flags);
 [DllImport("user32.dll")]private static extern bool EnumWindows(EnumProc callback,nint data);
 [DllImport("user32.dll")]private static extern uint GetWindowThreadProcessId(nint hwnd,out uint pid);
 [DllImport("user32.dll")]private static extern bool IsWindowVisible(nint hwnd);
 [DllImport("user32.dll")]private static extern nint GetWindow(nint hwnd,uint command);
 [DllImport("user32.dll")]private static extern nint GetForegroundWindow();
 [DllImport("user32.dll")]private static extern bool SetForegroundWindow(nint hwnd);
 [DllImport("user32.dll",SetLastError=true)]private static extern nint SetWindowLongPtrW(nint hwnd,int index,nint value);
 [DllImport("user32.dll")]private static extern nint GetDC(nint hwnd);
 [DllImport("user32.dll")]private static extern int ReleaseDC(nint hwnd,nint dc);
 [DllImport("gdi32.dll")]private static extern nint CreateCompatibleDC(nint dc);
 [DllImport("gdi32.dll")]private static extern nint CreateCompatibleBitmap(nint dc,int width,int height);
 [DllImport("gdi32.dll")]private static extern nint SelectObject(nint dc,nint obj);
 [DllImport("gdi32.dll")]private static extern bool BitBlt(nint dest,int x,int y,int width,int height,nint source,int sx,int sy,uint operation);
 [DllImport("gdi32.dll")]private static extern bool DeleteObject(nint obj);
 [DllImport("gdi32.dll")]private static extern bool DeleteDC(nint dc);
 [DllImport("user32.dll")]private static extern nint GetWindowLongPtrW(nint hwnd,int index);
 [DllImport("user32.dll")]private static extern bool GetWindowRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool GetClientRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool ClientToScreen(nint hwnd,ref POINT point);
}
