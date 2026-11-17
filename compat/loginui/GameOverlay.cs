// SPDX-License-Identifier: GPL-3.0-or-later
using System.ComponentModel;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Windows;
using System.Windows.Interop;
using System.Windows.Threading;
using Microsoft.Win32.SafeHandles;

namespace Oddward.LoginUi;

// Observes a PID + FILETIME identity with query and synchronize rights only.
// All window writes target our own HWND. Confirmed native ownership maintains
// z-order; rejected ownership waits hidden for a restricted external binder.
internal sealed class GameOverlay : IDisposable
{
 private static ulong nextRequestId;
 private UiEvent? bindingRequest;
 private bool bindingAcknowledged;
 private readonly LoginWindow window;
 private readonly Action<int> finish;
 private readonly Action<WindowInteropHelper,nint> setOwner;
 private readonly Action<UiEvent> requestOwner;
 private readonly Action recreateView;
 private readonly bool preferEmbedding;
 private readonly DispatcherTimer timer;
 private readonly WinEventProc windowEvent;
 private SafeProcessHandle? process;
 private GCHandle callbackRoot;
 private DispatcherOperation? pendingRefresh;
 private uint processId;
 private string creationTime="";
 private nint gameWindow,ownerAttemptedFor,locationHook,moveHook;
 private bool initialized,disposed,ownerRequested,refreshing,refreshAgain;
 private const uint QueryLimitedInformation=0x1000,Synchronize=0x00100000;
 private const uint WaitObject0=0,WaitTimeout=258;
 private const uint NoZOrder=0x0004,NoActivate=0x0010,NoOwnerZOrder=0x0200;
 private const uint LocationChange=0x800B,MoveSizeStart=0x000A,MoveSizeEnd=0x000B;

 internal GameOverlay(LoginWindow window,Action<int> finish,Action<WindowInteropHelper,nint>? setOwner=null,Action<UiEvent>? requestOwner=null,bool preferEmbedding=true,Action? recreateView=null)
 {
  this.window=window;this.finish=finish;this.setOwner=setOwner??((interop,owner)=>interop.Owner=owner);
  this.requestOwner=requestOwner??(_=>{});this.preferEmbedding=preferEmbedding&&requestOwner is not null;windowEvent=OnWindowEvent;
  this.recreateView=recreateView??(()=>finish(1));
  timer=new DispatcherTimer(DispatcherPriority.Background,window.Dispatcher){Interval=TimeSpan.FromMilliseconds(50)};
  timer.Tick+=OnTick;
  window.Closed+=OnClosed;
 }
 internal void Apply(Model model)
 {
  window.Dispatcher.VerifyAccess();
  if(disposed)return;
  if(initialized) {
   if(model.GameProcessId!=processId||model.GameCreationTime!=creationTime)throw new InvalidDataException("Login UI process identity changed.");
   return;
  }
  initialized=true;processId=model.GameProcessId;creationTime=model.GameCreationTime;
  if(processId==0){window.Show();return;}
  process=OpenProcess(QueryLimitedInformation|Synchronize,false,processId);
  if(process.IsInvalid||!GetProcessTimes(process,out ulong created,out _,out _,out _)||
     created!=ulong.Parse(creationTime,CultureInfo.InvariantCulture))throw new InvalidDataException("Login UI process identity unavailable.");
  window.UseGameOverlay();
  // OUTOFCONTEXT (0) does not inject a DLL. Install only after verifying identity;
  // callbacks arrive on this registering UI thread. Keep the delegate rooted.
  callbackRoot=GCHandle.Alloc(windowEvent);
  locationHook=SetWinEventHook(LocationChange,LocationChange,0,windowEvent,processId,0,0);
  moveHook=SetWinEventHook(MoveSizeStart,MoveSizeEnd,0,windowEvent,processId,0,0);
  if(locationHook==0&&moveHook==0)callbackRoot.Free();
  RefreshSafely();
  if(!disposed)timer.Start();
 }
 private void OnTick(object? sender,EventArgs e)=>RefreshSafely();
 private void OnWindowEvent(nint hook,uint kind,nint hwnd,int objectId,int childId,uint thread,uint time)
 {
  // Ignore accessibility descendants, stale targets and our own overlay events.
  if(disposed||hwnd==0||hwnd!=gameWindow||objectId!=0||childId!=0)return;
  if(kind!=LocationChange&&kind!=MoveSizeStart&&kind!=MoveSizeEnd)return;
  GetWindowThreadProcessId(hwnd,out uint pid);if(pid!=processId)return;
  RefreshSafely();
 }
 private void RefreshSafely()
 {
  if(disposed)return;
  // Native window operations can pump messages and reenter WinEvent callbacks.
  // Finish the current update first, then coalesce any reentry into one UI turn.
  if(refreshing){refreshAgain=true;return;}
  refreshing=true;refreshAgain=false;
  try{Refresh();}catch(Exception){if(!disposed)finish(1);}
  finally {
   refreshing=false;
   if(refreshAgain&&!disposed&&pendingRefresh is null&&!window.Dispatcher.HasShutdownStarted) {
    refreshAgain=false;
    pendingRefresh=window.Dispatcher.BeginInvoke(DispatcherPriority.Send,new Action(()=>{pendingRefresh=null;RefreshSafely();}));
   }
  }
 }
 private void Refresh()
 {
  if(disposed||process is null)return;
  // GetProcessTimes.ExitTime is undefined while a process is alive. A held
  // process handle signals actual exit, including an exit code of STILL_ACTIVE.
  uint wait=WaitForSingleObject(process,0);
  if(wait==WaitObject0){finish(0);return;}
  if(wait!=WaitTimeout){finish(1);return;}
  // A broker transaction may temporarily expose WS_CHILD without its final
  // parent. Until the exact ACK, preserve this HWND and request even if the
  // visible game target changes; only process exit can end the wait here.
  if(ownerRequested&&!bindingAcknowledged){Hide();return;}
  if(!Candidate(gameWindow,processId))gameWindow=FindMainWindow(processId);
  if(gameWindow==0||IsIconic(gameWindow)||!ClientBounds(gameWindow,out RECT bounds)){Hide();return;}
  WindowInteropHelper interop=new(window);
  // Create our own HWND before attempting an external owner. Windows can reject
  // either operation across integrity levels. WPF caches even rejected owners.
  nint own=EnsureHandle(interop);
  // The restricted binder never migrates an existing child from another live
  // parent. Replace only our view, preserving its form in the controller.
  if((GetWindowLongPtrW(own,-16).ToInt64()&0x40000000L)!=0&&GetParent(own)!=gameWindow) {
   Hide();recreateView();return;
  }
  if(ownerAttemptedFor!=gameWindow) {
   // Every target round starts hidden, including reparenting a visible child.
   Hide();ownerAttemptedFor=gameWindow;ownerRequested=false;bindingRequest=null;bindingAcknowledged=false;
   if(!preferEmbedding){try{setOwner(interop,gameWindow);}catch(Win32Exception){}}
  }
  if(preferEmbedding&&!ownerRequested){RequestBinding(own,"embed_overlay");return;}
  long style=GetWindowLongPtrW(own,-16).ToInt64();
  bool child=(style&0x40000000L)!=0&&(style&0x80000000L)==0&&GetParent(own)==gameWindow;
  if(!child&&GetWindow(own,4)!=gameWindow) {
   Hide();if(!ownerRequested)RequestBinding(own,"bind_overlay");return;
  }
  if(!child) {
   // Managed Owner is a cache, never proof of binding. Do not write it for a
   // native child: GWLP_HWNDPARENT would change its actual parent instead.
   if(interop.Owner!=gameWindow){try{interop.Owner=gameWindow;}catch(Win32Exception){}}
   if(GetWindow(own,4)!=gameWindow){Hide();return;}
  }
  // Child position is parent-client relative. Windows carries it on parent
  // moves; the event/timer path still observes resize and process lifetime.
  if(!GetWindowRect(own,out RECT current)||!current.Equals(bounds))
   if(!SetWindowPos(own,0,child?0:bounds.Left,child?0:bounds.Top,bounds.Right-bounds.Left,bounds.Bottom-bounds.Top,NoZOrder|NoActivate|NoOwnerZOrder))throw new Win32Exception();
  // Native calls may reenter parent destruction and dispose this tracker.
  if(!disposed&&!window.IsVisible)window.Show();
 }
 private nint EnsureHandle(WindowInteropHelper interop)
 {
  if(interop.Handle!=0)return interop.Handle;
  nint previous=0;
  try {
   nint context=GetWindowDpiAwarenessContext(gameWindow);
   if(context!=0)previous=SetThreadDpiAwarenessContext(context);
  } catch(EntryPointNotFoundException) { }
  // Scope only synchronous HWND creation. Never change an existing HWND's DPI
  // context; unsupported contexts stay at the default and the binder uses owner.
  try{return interop.EnsureHandle();}
  finally{if(previous!=0&&SetThreadDpiAwarenessContext(previous)==0)throw new Win32Exception();}
 }
 private void RequestBinding(nint own,string kind)
 {
  Hide();ownerRequested=true;
  bindingAcknowledged=false;
  bindingRequest=new UiEvent(kind,Window:((ulong)(nuint)own).ToString(CultureInfo.InvariantCulture),GameWindow:((ulong)(nuint)gameWindow).ToString(CultureInfo.InvariantCulture),RequestId:checked(++nextRequestId).ToString(CultureInfo.InvariantCulture));
  requestOwner(bindingRequest);
 }
 internal void WindowBound(UiEvent completed)
 {
  window.Dispatcher.VerifyAccess();
  if(disposed||bindingRequest is null||completed!=bindingRequest)return;
  bindingAcknowledged=true;RefreshSafely();
 }
 private void Hide(){if(window.IsVisible)window.Hide();}
 private static bool Candidate(nint hwnd,uint pid)
 {
  if(hwnd==0||!IsWindowVisible(hwnd)||GetWindow(hwnd,4)!=0)return false;
  GetWindowThreadProcessId(hwnd,out uint found);
  if(found!=pid||(GetWindowLongPtrW(hwnd,-20).ToInt64()&0x80)!=0)return false;
  return DwmGetWindowAttribute(hwnd,14,out int cloaked,sizeof(int))!=0||cloaked==0;
 }
 private static nint FindMainWindow(uint pid)
 {
  nint selected=0;long largest=0;
  EnumWindows((hwnd,_)=>{
   if(Candidate(hwnd,pid)&&GetClientRect(hwnd,out RECT r)){
    long area=(long)(r.Right-r.Left)*(r.Bottom-r.Top);
    if(area>largest){selected=hwnd;largest=area;}
   }
   return true;
  },0);
  return selected;
 }
 private static bool ClientBounds(nint hwnd,out RECT rect)
 {
  rect=default;
  if(!GetClientRect(hwnd,out RECT local)||local.Right<=0||local.Bottom<=0)return false;
  POINT origin=new();if(!ClientToScreen(hwnd,ref origin))return false;
  rect=new RECT{Left=origin.X,Top=origin.Y,Right=origin.X+local.Right,Bottom=origin.Y+local.Bottom};return true;
 }
 private void OnClosed(object? sender,EventArgs e)=>Dispose();
 public void Dispose()
 {
  if(disposed)return;window.Dispatcher.VerifyAccess();disposed=true;
  timer.Stop();timer.Tick-=OnTick;window.Closed-=OnClosed;
  pendingRefresh?.Abort();pendingRefresh=null;
  // WinEvent hooks must be removed on their registering thread, before unrooting.
  if(locationHook!=0){UnhookWinEvent(locationHook);locationHook=0;}
  if(moveHook!=0){UnhookWinEvent(moveHook);moveHook=0;}
  if(callbackRoot.IsAllocated)callbackRoot.Free();
  process?.Dispose();process=null;
 }
 [StructLayout(LayoutKind.Sequential)]private struct RECT{internal int Left,Top,Right,Bottom;}
 [StructLayout(LayoutKind.Sequential)]private struct POINT{internal int X,Y;}
 private delegate bool EnumProc(nint hwnd,nint data);
 private delegate void WinEventProc(nint hook,uint kind,nint hwnd,int objectId,int childId,uint thread,uint time);
 [DllImport("kernel32.dll",SetLastError=true)]private static extern SafeProcessHandle OpenProcess(uint access,bool inherit,uint pid);
 [DllImport("kernel32.dll",SetLastError=true)]private static extern bool GetProcessTimes(SafeProcessHandle process,out ulong created,out ulong exited,out ulong kernel,out ulong user);
 [DllImport("kernel32.dll",SetLastError=true)]private static extern uint WaitForSingleObject(SafeProcessHandle process,uint milliseconds);
 [DllImport("user32.dll")]private static extern nint SetWinEventHook(uint first,uint last,nint module,WinEventProc callback,uint pid,uint thread,uint flags);
 [DllImport("user32.dll")]private static extern bool UnhookWinEvent(nint hook);
 [DllImport("user32.dll")]private static extern bool EnumWindows(EnumProc callback,nint data);
 [DllImport("user32.dll")]private static extern uint GetWindowThreadProcessId(nint hwnd,out uint pid);
 [DllImport("user32.dll")]private static extern bool IsWindowVisible(nint hwnd);
 [DllImport("user32.dll")]private static extern bool IsIconic(nint hwnd);
 [DllImport("user32.dll")]private static extern nint GetWindow(nint hwnd,uint command);
 [DllImport("user32.dll")]private static extern nint GetParent(nint hwnd);
 [DllImport("user32.dll")]private static extern nint GetWindowDpiAwarenessContext(nint hwnd);
 [DllImport("user32.dll",SetLastError=true)]private static extern nint SetThreadDpiAwarenessContext(nint context);
 [DllImport("user32.dll")]private static extern nint GetWindowLongPtrW(nint hwnd,int index);
 [DllImport("user32.dll")]private static extern bool GetWindowRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool GetClientRect(nint hwnd,out RECT rect);
 [DllImport("user32.dll")]private static extern bool ClientToScreen(nint hwnd,ref POINT point);
 [DllImport("user32.dll",SetLastError=true)]private static extern bool SetWindowPos(nint hwnd,nint insertAfter,int x,int y,int width,int height,uint flags);
 [DllImport("dwmapi.dll")]private static extern int DwmGetWindowAttribute(nint hwnd,uint attribute,out int value,int size);
}
