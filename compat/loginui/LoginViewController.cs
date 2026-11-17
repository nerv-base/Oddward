// SPDX-License-Identifier: GPL-3.0-or-later
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Windows;
using System.Windows.Threading;
using Microsoft.Win32.SafeHandles;

namespace Oddward.LoginUi;

// One login session can outlive its native parent HWND. A destroyed WPF Window
// is never reused; the verified process identity remains held until the session
// ends. The controller has no credential persistence or network access.
internal sealed class LoginViewController:IDisposable
{
 private readonly Application app;
 private readonly Action<UiEvent> emit;
 private readonly Action<int> finish;
 private SafeProcessHandle? gameProcess;
 private Model? model;
 private GameOverlay? overlay;
 private LoginWindow.FormState? form;
 private DispatcherOperation? recreation;
 private bool stopped,replacingView;
 internal LoginWindow? Window {get;private set;}
 internal Dispatcher Dispatcher=>app.Dispatcher;
 internal LoginViewController(Application app,Action<UiEvent> emit,Action<int> finish){this.app=app;this.emit=emit;this.finish=finish;}
 internal void Apply(Model value)
 {
  Dispatcher.VerifyAccess();if(stopped)return;
  if(model is null) {
   if(value.GameProcessId!=0) {
    gameProcess=OpenProcess(0x00101000,false,value.GameProcessId);
    if(gameProcess.IsInvalid||!GetProcessTimes(gameProcess,out ulong created,out _,out _,out _)||created!=ulong.Parse(value.GameCreationTime,CultureInfo.InvariantCulture))throw new InvalidDataException("Login UI process identity unavailable.");
   }
  } else if(value.GameProcessId!=model.GameProcessId||value.GameCreationTime!=model.GameCreationTime)throw new InvalidDataException("Login UI process identity changed.");
  model=value;
  if(form is not null)form.Pending=false; // A fresh backend snapshot clears an in-flight local action.
  if(Window is null){if(recreation is null&&!replacingView)CreateView();return;}
  Window.Apply(value);overlay!.Apply(value);
 }
 private void CreateView()
 {
  if(stopped||model is null)return;
  LoginWindow window=new(emit);Window=window;app.MainWindow=window;
  // Observe Closed before GameOverlay disposes its per-window tracking state.
  window.Closed+=OnClosed;
  overlay=new GameOverlay(window,Stop,requestOwner:emit,recreateView:RecreateView);
  window.Apply(model);if(form is not null){window.RestoreForm(form);form=null;}overlay.Apply(model);
 }
 private void OnClosed(object? sender,EventArgs args)
 {
  if(stopped||sender is not LoginWindow window||window!=Window)return;
  window.Closed-=OnClosed;Window=null;overlay?.Dispose();overlay=null;
  if(!window.InfrastructureLost){emit(new UiEvent("cancel"));Stop(0);return;}
  form=window.CaptureForm();QueueRecreation();
 }
 private void RecreateView()
 {
  if(stopped||replacingView||Window is not LoginWindow window)return;
  replacingView=true;
  try {
   form=window.CaptureForm();window.Closed-=OnClosed;Window=null;
   overlay?.Dispose();overlay=null;
   // Closing our child must neither cancel the session nor touch its parent.
   window.Close();
  } catch(Exception){Stop(1);}
  finally{replacingView=false;}
  QueueRecreation();
 }
 private void QueueRecreation()
 {
  if(stopped||recreation is not null)return;
  if(gameProcess is null||WaitForSingleObject(gameProcess,0)!=258){Stop(0);return;}
  // Parent destruction delivers Closed before WM_NCDESTROY. Wait for the
  // current native dispatch to finish before creating another Window object.
  recreation=Dispatcher.BeginInvoke(DispatcherPriority.Background,new Action(()=>{
   recreation=null;if(stopped)return;
   if(gameProcess is null||WaitForSingleObject(gameProcess,0)!=258){Stop(0);return;}
   try{CreateView();}catch(Exception){Stop(1);}
  }));
 }
 internal void WindowBound(UiEvent completed){if(!stopped)overlay?.WindowBound(completed);}
 internal void Stop(int code)
 {
  if(stopped)return;Dispose();finish(code);
 }
 public void Dispose()
 {
  if(stopped)return;Dispatcher.VerifyAccess();stopped=true;
  recreation?.Abort();recreation=null;overlay?.Dispose();overlay=null;
  if(Window is LoginWindow window){Window=null;window.Closed-=OnClosed;window.Close();}
  gameProcess?.Dispose();gameProcess=null;form=null;model=null;
 }
 [DllImport("kernel32.dll",SetLastError=true)]private static extern SafeProcessHandle OpenProcess(uint access,bool inherit,uint pid);
 [DllImport("kernel32.dll",SetLastError=true)]private static extern bool GetProcessTimes(SafeProcessHandle process,out ulong created,out ulong exited,out ulong kernel,out ulong user);
 [DllImport("kernel32.dll",SetLastError=true)]private static extern uint WaitForSingleObject(SafeProcessHandle process,uint milliseconds);
}
