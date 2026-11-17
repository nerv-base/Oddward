// SPDX-License-Identifier: GPL-3.0-or-later
using System.IO;
using System.Windows;
using System.Windows.Threading;

namespace Oddward.LoginUi;

// Process entry point. The helper is a presentation-only child of the launcher:
// it reads snapshots on stdin, renders them, and writes user intents on stdout.
// It never touches the network or credential store. Game identity and window
// geometry are observed read-only; only the helper's own window is positioned.
internal static class Program
{
 private static readonly object writeLock = new();
 private static Stream? output;
 private static int status;
 private static bool finished;

 [STAThread]
 private static int Main()
 {
  Stream input;
  try {
   input = Console.OpenStandardInput();
   output = Console.OpenStandardOutput();
  } catch (IOException) { return 1; }
  Application app = new() { ShutdownMode = ShutdownMode.OnExplicitShutdown };
  app.Startup += (_, _) => Begin(app, input);
  try { app.Run(); } catch (Exception) { return 1; }
  return status;
 }

 private static void Begin(Application app, Stream input)
 {
  void Finish(int code)
  {
   lock (writeLock) { if (finished) return; finished = true; status = code; }
   app.Shutdown(code);
  }
  bool Emit(UiEvent e)
  {
   lock (writeLock) {
    if (finished) return false;
    try { Protocol.Write(output!, new Envelope(Type: "event", Event: e)); return true; }
    catch (Exception) { finished = true; status = 1; return false; }
   }
  }
  LoginViewController view = new(app, e => { if (!Emit(e)) app.Shutdown(1); }, Finish);
  app.Exit += (_, _) => view.Dispose();
  if (!Emit(new UiEvent("ready"))) { Finish(1); return; }
  Thread reader = new(() => Read(input, view, view.Stop)) { IsBackground = true, Name = "login-ui-reader" };
  reader.Start();
 }

 // Frames arrive on a background thread; every snapshot is applied on the UI
 // thread that owns the window. A malformed or unknown frame ends the helper:
 // the launcher then reports a failed login window instead of guessing.
 private static void Read(Stream input, LoginViewController view, Action<int> finish)
 {
  Dispatcher dispatcher = view.Dispatcher;
  try {
   while (true) {
    Envelope? frame = Protocol.Read(input);
    if (frame is null) { dispatcher.BeginInvoke(() => finish(0)); return; }
    switch (frame.Type) {
     case "model": { Model model = frame.Model!; dispatcher.BeginInvoke(() => {
      if(finished)return;
      try{view.Apply(model);}catch(Exception){finish(1);}
     }); break; }
     case "window_bound": { UiEvent completed=frame.Event!;dispatcher.BeginInvoke(()=>{if(!finished)view.WindowBound(completed);});break; }
     case "close": dispatcher.BeginInvoke(() => finish(0)); return;
     default: dispatcher.BeginInvoke(() => finish(1)); return;
    }
   }
  } catch (Exception) {
   try { dispatcher.BeginInvoke(() => finish(1)); } catch (InvalidOperationException) { }
  }
 }
}
