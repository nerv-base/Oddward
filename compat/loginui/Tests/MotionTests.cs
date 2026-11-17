// SPDX-License-Identifier: GPL-3.0-or-later
using System.Collections.Concurrent;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Runtime.CompilerServices;
using System.Windows;
using System.Windows.Interop;
using System.Windows.Media;

namespace Oddward.LoginUi;

// Native geometry measurements on separate, nonactivating, off-screen fixtures.
// These measure asynchronous tracking, not DWM frame presentation or real games.
internal static class MotionTests
{
 internal static void Run(string helper,string output,Action<string,Action> check)
 {
  check("event driven overlay follows high frequency fixture motion and final geometry",()=>{
   using Process game=Start(Environment.ProcessPath!,"--motion-fixture");
   Process? child=null;
   try {
    Require(ReadLine(game)=="ready","motion fixture ready");
    child=Start(helper);Process ui=child;
    Task<Envelope?> ready=Task.Run(()=>Protocol.Read(ui.StandardOutput.BaseStream));
    Until(()=>ready.IsCompleted,"helper ready frame");Require(ready.Result?.Event?.Kind=="ready","helper reports ready");
    Protocol.Write(ui.StandardInput.BaseStream,new Envelope(Type:"model",Model:new Model{Screen="phone",GameProcessId=(uint)game.Id,GameCreationTime=game.StartTime.ToUniversalTime().ToFileTimeUtc().ToString(CultureInfo.InvariantCulture)}));
    Task<Envelope?> binding=Task.Run(()=>Protocol.Read(ui.StandardOutput.BaseStream));Until(()=>binding.IsCompleted,"motion helper requests binding");
     UiEvent request=binding.Result!.Event!;OverlayTests.BindFixtureOwner(request,ui.Id,(uint)game.Id);Protocol.Write(ui.StandardInput.BaseStream,new Envelope(Type:"window_bound",Event:request));
     nint overlay=0,gameWindow=OverlayTests.VisibleWindow((uint)game.Id);
    Until(()=>{overlay=OverlayTests.VisibleWindow((uint)ui.Id);return overlay!=0&&Bounds(overlay)==ClientBounds(gameWindow);},"motion overlay initially aligned");
    Require(GetWindow(overlay,4)==gameWindow,"motion helper has actual native game owner");
    ConcurrentQueue<Sample> incoming=new();List<Sample> source=[];Dictionary<Rect,Sample> pending=[];List<double> observed=[];
    Task read=Task.Run(()=>{while(true){string? line=game.StandardOutput.ReadLine();if(line=="done")return;if(line is null)throw new IOException("motion stream ended");incoming.Enqueue(Parse(line));}});
    game.StandardInput.WriteLine("motion");game.StandardInput.Flush();
    Stopwatch run=Stopwatch.StartNew();Rect final=default;
    while(run.ElapsedMilliseconds<15000) {
     while(incoming.TryDequeue(out Sample? sample)){source.Add(sample);pending.Add(sample.Bounds,sample);final=sample.Bounds;}
     Rect actual=Bounds(overlay);
     if(pending.Remove(actual,out Sample? match))observed.Add(MillisecondsSince(match.Time));
     if(read.IsCompleted&&incoming.IsEmpty&&source.Count==80&&actual==final)break;
     OverlayTests.Pump();Thread.Sleep(1);
    }
    Require(read.IsCompletedSuccessfully&&source.Count==80,"all high frequency source moves delivered");
    bool finalAligned=Bounds(overlay)==final;
    List<double> steps=[];
    for(int i=0;i<16;i++) {
     game.StandardInput.WriteLine("step");game.StandardInput.Flush();Sample sample=Parse(ReadLine(game));
     Until(()=>Bounds(overlay)==sample.Bounds,"individual move reaches exact geometry");
     steps.Add(MillisecondsSince(sample.Time));Thread.Sleep(8);
    }
    double[] intervals=source.Zip(source.Skip(1),(a,b)=>(b.Time-a.Time)*1000.0/Stopwatch.Frequency).ToArray();
    File.WriteAllLines(Path.Combine(output,"overlay-motion.txt"),new[]{
     "Evidence: off-screen separate ordinary processes; native HWND geometry, not compositor frame timing.",
     $"Requested source interval: 8 ms; source frames: {source.Count}; actual interval median/p95: {Percentile(intervals,.5):F2}/{Percentile(intervals,.95):F2} ms",
     $"Matched continuous source positions: {observed.Count}/{source.Count}; fraction: {observed.Count/(double)source.Count:F3}; exact final geometry: {finalAligned}",
     $"Observed continuous matches latency median/p95/max: {Percentile(observed,.5):F2}/{Percentile(observed,.95):F2}/{observed.DefaultIfEmpty().Max():F2} ms (unmatched frames excluded)",
     $"Individual step latency median/p95/max: {Percentile(steps,.5):F2}/{Percentile(steps,.95):F2}/{steps.Max():F2} ms",
     $"Overlay remains non-topmost: {(GetWindowLongPtrW(overlay,-20).ToInt64()&8)==0}"
    });
    Require(finalAligned,"continuous motion ends exactly at the game client");
    Require(observed.Count>=source.Count*.85,$"tracks at least 85% of continuous positions: {observed.Count}/{source.Count}");
    Require(Percentile(steps,.95)<30,$"individual move p95 below 30ms: {Percentile(steps,.95):F2}ms");
    Require((GetWindowLongPtrW(overlay,-20).ToInt64()&8)==0,"motion never enables topmost");
   } finally {
    if(child is not null){try{child.StandardInput.Close();Until(()=>child.HasExited,"motion helper closes");}finally{if(!child.HasExited){child.Kill();child.WaitForExit(5000);}child.Dispose();}}
    game.StandardInput.Close();try{Until(()=>game.HasExited,"motion fixture closes");}finally{if(!game.HasExited){game.Kill();game.WaitForExit(5000);}}
   }
  });
  check("disposed overlay tracker stops following and releases callback roots",()=>{
   Window game=new(){Title="Oddward disposal fixture",ShowActivated=false,Width=620,Height=440,Left=-4000,Top=-4000};
   LoginWindow overlay=new(_=>{});
   try {
    game.Show();nint gameWindow=new WindowInteropHelper(game).Handle;
    WeakReference tracker=CreateDisposedTracker(overlay,gameWindow);
    nint own=new WindowInteropHelper(overlay).Handle;Rect frozen=Bounds(own);
    Require(SetWindowPos(gameWindow,0,-3700,-3900,700,480,0x0014),"move disposed tracker's own fixture");
    Stopwatch elapsed=Stopwatch.StartNew();while(elapsed.ElapsedMilliseconds<180){OverlayTests.Pump();Thread.Sleep(1);}
    Require(Bounds(own)==frozen,"disposed tracker no longer follows game geometry");
    GC.Collect();GC.WaitForPendingFinalizers();GC.Collect();
    Require(!tracker.IsAlive,"disposed tracker is collectible after releasing native callback roots");
   } finally {overlay.Close();game.Close();OverlayTests.Pump();}
  });
 }
 [MethodImpl(MethodImplOptions.NoInlining)]
 private static WeakReference CreateDisposedTracker(LoginWindow overlay,nint game)
 {
  using Process self=Process.GetCurrentProcess();
  GameOverlay tracker=new(overlay,code=>throw new InvalidOperationException("tracker unexpectedly exited: "+code));
  try {
   Model model=new(){Screen="phone",GameProcessId=(uint)self.Id,GameCreationTime=self.StartTime.ToUniversalTime().ToFileTimeUtc().ToString(CultureInfo.InvariantCulture)};
   overlay.Apply(model);tracker.Apply(model);
   Until(()=>overlay.IsVisible&&Bounds(new WindowInteropHelper(overlay).Handle)==ClientBounds(game),"disposal tracker aligned before disposal");
   return new WeakReference(tracker);
  } finally {tracker.Dispose();}
 }
 internal static int FixtureMain()
 {
  Application app=new(){ShutdownMode=ShutdownMode.OnExplicitShutdown};
  Window game=new(){Title="Oddward off-screen motion fixture",ShowActivated=false,Width=620,Height=440,Left=-4000,Top=-4000,Background=Brushes.CornflowerBlue};
  app.Startup+=(_,_)=>{
   game.Show();nint hwnd=new WindowInteropHelper(game).Handle;Console.WriteLine("ready");Console.Out.Flush();
   Thread reader=new(()=>{
    try {
     int sequence=0;
     void Move(){app.Dispatcher.Invoke(()=>{
      int i=sequence++;Require(SetWindowPos(hwnd,0,-4000+i*3,-4000+i%17,620+i%7*4,440+i%5*4,0x0014),"move only own fixture");
      Rect rect=ClientBounds(hwnd);long time=Stopwatch.GetTimestamp();
      Console.WriteLine(FormattableString.Invariant($"move {i} {time} {(int)rect.X} {(int)rect.Y} {(int)rect.Width} {(int)rect.Height}"));Console.Out.Flush();
     });}
     while(Console.ReadLine() is string command) {
      if(command=="motion"){for(int i=0;i<80;i++){Move();Thread.Sleep(8);}Console.WriteLine("done");Console.Out.Flush();}
      else if(command=="step")Move();else break;
     }
     app.Dispatcher.BeginInvoke(()=>app.Shutdown());
    } catch(Exception error) when(error is TaskCanceledException or InvalidOperationException){}
   }){IsBackground=true};reader.Start();
  };
  return app.Run();
 }
 private sealed record Sample(long Time,Rect Bounds);
 private static Sample Parse(string line){string[] fields=line.Split(' ');Require(fields.Length==7&&fields[0]=="move","motion sample framing");long[] values=fields.Skip(1).Select(x=>long.Parse(x,CultureInfo.InvariantCulture)).ToArray();return new(values[1],new Rect(values[2],values[3],values[4],values[5]));}
 private static Process Start(string path,string? argument=null){ProcessStartInfo start=new(path){UseShellExecute=false,RedirectStandardInput=true,RedirectStandardOutput=true,CreateNoWindow=true};if(argument is not null)start.ArgumentList.Add(argument);return Process.Start(start)!;}
 private static string ReadLine(Process process){Task<string?> read=process.StandardOutput.ReadLineAsync();Until(()=>read.IsCompleted,"fixture line arrives");return read.GetAwaiter().GetResult()??throw new IOException("fixture stream ended");}
 private static void Until(Func<bool> condition,string message){Stopwatch elapsed=Stopwatch.StartNew();while(elapsed.ElapsedMilliseconds<5000){OverlayTests.Pump();if(condition())return;Thread.Sleep(1);}throw new InvalidOperationException(message);}
 private static void Require(bool condition,string message){if(!condition)throw new InvalidOperationException(message);}
 private static double MillisecondsSince(long time)=>(Stopwatch.GetTimestamp()-time)*1000.0/Stopwatch.Frequency;
 private static double Percentile(IEnumerable<double> values,double fraction){double[] sorted=values.Order().ToArray();return sorted.Length==0?double.PositiveInfinity:sorted[(int)Math.Ceiling((sorted.Length-1)*fraction)];}
 private static Rect Bounds(nint hwnd){GetWindowRect(hwnd,out RECT rect);return new Rect(rect.Left,rect.Top,rect.Right-rect.Left,rect.Bottom-rect.Top);}
 private static Rect ClientBounds(nint hwnd){GetClientRect(hwnd,out RECT rect);POINT point=new();ClientToScreen(hwnd,ref point);return new Rect(point.X,point.Y,rect.Right,rect.Bottom);}
 [StructLayout(LayoutKind.Sequential)]private struct RECT{internal int Left,Top,Right,Bottom;}
 [StructLayout(LayoutKind.Sequential)]private struct POINT{internal int X,Y;}
 [DllImport("user32.dll")]private static extern bool GetWindowRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool GetClientRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool ClientToScreen(nint hwnd,ref POINT point);
 [DllImport("user32.dll")]private static extern nint GetWindow(nint hwnd,uint command);
 [DllImport("user32.dll")]private static extern nint GetWindowLongPtrW(nint hwnd,int index);
 [DllImport("user32.dll")]private static extern bool SetWindowPos(nint hwnd,nint after,int x,int y,int width,int height,uint flags);
}
